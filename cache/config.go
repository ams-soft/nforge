package cache

import (
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Drivers.
const (
	DriverRedis  = "redis"
	DriverMemory = "memory"
)

// Object codecs (short names of the framework codec registry).
const (
	CodecJSON = "json"
	CodecCBOR = "cbor"
)

// Defaults and bounds of the configuration.
const (
	defaultMaxKeyBytes      = 1024
	defaultMaxValueBytes    = 1 * MB
	defaultMaxInflightLoads = 1024
	defaultLoadTimeout      = 5 * time.Second

	maxKeyBytesLimit   = 64 * 1024
	maxValueBytesLimit = 64 * MB

	defaultRedisHost       = "localhost"
	defaultRedisPort       = 6379
	defaultPoolSize        = 32
	defaultMinIdleConns    = 2
	defaultMaxIdleConns    = 32
	defaultMaxActiveConns  = 32
	defaultMaxConnIdleTime = 5 * time.Minute
	defaultMaxConnLifetime = 30 * time.Minute
	defaultDialTimeout     = time.Second
	defaultReadTimeout     = time.Second
	defaultWriteTimeout    = time.Second
	defaultPoolTimeout     = 500 * time.Millisecond
	defaultOpTimeout       = 2 * time.Second
	defaultReadRetries     = 1
	maxReadRetries         = 3
	defaultMinBackoff      = 50 * time.Millisecond
	defaultMaxBackoff      = 500 * time.Millisecond

	defaultMaxEntries      = 10000
	defaultMemoryMaxBytes  = 64 * MB
	defaultCleanupInterval = 30 * time.Second
)

// Config is the `cache:` section. It is standalone (no framework imports):
// config.Config embeds it and deps opens it; other programs can call Open
// directly. Zero values of caps and timeouts take the defaults below; see
// ApplyDefaults.
type Config struct {
	Enabled     bool          `yaml:"enabled"`      // default: false (deps.Cache is nil)
	Driver      string        `yaml:"driver"`       // redis (default) | memory
	Codec       string        `yaml:"codec"`        // object codec: json (default) | cbor; must be enabled in the codec registry
	Prefix      string        `yaml:"prefix"`       // optional; "" derives nforge:<env>:[<tenant>:]<app.name>:
	Limits      LimitsConfig  `yaml:"limits"`       //
	LoadTimeout time.Duration `yaml:"load_timeout"` // bounds a shared GetOrLoad loader; default: 5s
	Redis       RedisConfig   `yaml:"redis"`        // used only when driver is redis
	Memory      MemoryConfig  `yaml:"memory"`       // used only when driver is memory
}

// LimitsConfig bounds keys, values and concurrent loads.
type LimitsConfig struct {
	MaxKeyBytes      int      `yaml:"max_key_bytes"`      // full resolved key (prefix included); default: 1024, at most 64 KiB
	MaxValueBytes    ByteSize `yaml:"max_value_bytes"`    // encoded or raw value; default: 1mb, at most 64 MiB
	MaxInflightLoads int      `yaml:"max_inflight_loads"` // distinct concurrent GetOrLoad flights; default: 1024
}

// RedisConfig configures the Redis driver. Never log it with a plain
// formatter expecting secrets: Format and LogValue redact the URL, the
// password and the CA file path.
type RedisConfig struct {
	URL      string          `yaml:"url"`      // optional redis:// or rediss:// URL; wins for address, credentials, database and TLS
	Host     string          `yaml:"host"`     // default: localhost
	Port     int             `yaml:"port"`     // default: 6379
	Username string          `yaml:"username"` // optional (ACL user)
	Password string          `yaml:"password"` // optional; no AUTH is sent when username and password are empty
	Database int             `yaml:"database"` // default: 0
	TLS      RedisTLSConfig  `yaml:"tls"`      //
	Pool     RedisPoolConfig `yaml:"pool"`     //
	Timeouts RedisTimeouts   `yaml:"timeouts"` //
	Retry    RedisRetry      `yaml:"retry"`    //
}

// RedisTLSConfig enables TLS with hostname verification and TLS 1.2+.
// There is deliberately no option to skip verification.
type RedisTLSConfig struct {
	Enabled    bool   `yaml:"enabled"`     // default: false; a rediss:// URL always enables TLS
	ServerName string `yaml:"server_name"` // default: the Redis host
	CAFile     string `yaml:"ca_file"`     // PEM roots; default: the system roots
}

// RedisPoolConfig sizes the connection pool.
type RedisPoolConfig struct {
	Size            int           `yaml:"size"`               // default: 32
	MinIdleConns    int           `yaml:"min_idle_conns"`     // default: 2
	MaxIdleConns    int           `yaml:"max_idle_conns"`     // default: 32
	MaxActiveConns  int           `yaml:"max_active_conns"`   // hard cap of open connections; default: 32
	MaxConnIdleTime time.Duration `yaml:"max_conn_idle_time"` // default: 5m
	MaxConnLifetime time.Duration `yaml:"max_conn_lifetime"`  // default: 30m
}

// RedisTimeouts bound every phase of an operation; a caller deadline that
// is earlier always wins.
type RedisTimeouts struct {
	Dial      time.Duration `yaml:"dial"`      // default: 1s
	Read      time.Duration `yaml:"read"`      // default: 1s
	Write     time.Duration `yaml:"write"`     // default: 1s
	Pool      time.Duration `yaml:"pool"`      // wait for a free connection; default: 500ms
	Operation time.Duration `yaml:"operation"` // whole operation, retries included; default: 2s
}

// RedisRetry configures the retries of pure reads (Get, GetBytes, Exists,
// TTL, Ping) on transport failures. Mutations are never retried.
type RedisRetry struct {
	ReadRetries *int          `yaml:"read_retries"` // extra read attempts, 0..3; default: 1 (explicit 0 disables)
	MinBackoff  time.Duration `yaml:"min_backoff"`  // default: 50ms
	MaxBackoff  time.Duration `yaml:"max_backoff"`  // default: 500ms
}

// MemoryConfig bounds the in-process driver. The budget counts key and
// value bytes only, not the Go map/list/allocator overhead: it is not a
// hard cap on the process memory.
type MemoryConfig struct {
	MaxEntries      int           `yaml:"max_entries"`      // default: 10000
	MaxBytes        ByteSize      `yaml:"max_bytes"`        // key + value bytes; default: 64mb
	CleanupInterval time.Duration `yaml:"cleanup_interval"` // expired-entry sweep; default: 30s
}

// ApplyDefaults fills the zero values with the defaults. read_retries is a
// pointer so that an explicit 0 (no retry) differs from "unset".
func ApplyDefaults(cfg *Config) {
	cfg.Driver = strings.ToLower(strings.TrimSpace(cfg.Driver))
	if cfg.Driver == "" {
		cfg.Driver = DriverRedis
	}
	cfg.Codec = strings.ToLower(strings.TrimSpace(cfg.Codec))
	if cfg.Codec == "" {
		cfg.Codec = CodecJSON
	}
	setDefault(&cfg.Limits.MaxKeyBytes, defaultMaxKeyBytes)
	setDefault(&cfg.Limits.MaxValueBytes, defaultMaxValueBytes)
	setDefault(&cfg.Limits.MaxInflightLoads, defaultMaxInflightLoads)
	setDefault(&cfg.LoadTimeout, defaultLoadTimeout)

	r := &cfg.Redis
	if r.URL == "" {
		setDefault(&r.Host, defaultRedisHost)
		setDefault(&r.Port, defaultRedisPort)
	}
	setDefault(&r.Pool.Size, defaultPoolSize)
	setDefault(&r.Pool.MinIdleConns, defaultMinIdleConns)
	setDefault(&r.Pool.MaxIdleConns, defaultMaxIdleConns)
	setDefault(&r.Pool.MaxActiveConns, defaultMaxActiveConns)
	setDefault(&r.Pool.MaxConnIdleTime, defaultMaxConnIdleTime)
	setDefault(&r.Pool.MaxConnLifetime, defaultMaxConnLifetime)
	setDefault(&r.Timeouts.Dial, defaultDialTimeout)
	setDefault(&r.Timeouts.Read, defaultReadTimeout)
	setDefault(&r.Timeouts.Write, defaultWriteTimeout)
	setDefault(&r.Timeouts.Pool, defaultPoolTimeout)
	setDefault(&r.Timeouts.Operation, defaultOpTimeout)
	if r.Retry.ReadRetries == nil {
		n := defaultReadRetries
		r.Retry.ReadRetries = &n
	}
	setDefault(&r.Retry.MinBackoff, defaultMinBackoff)
	setDefault(&r.Retry.MaxBackoff, defaultMaxBackoff)

	setDefault(&cfg.Memory.MaxEntries, defaultMaxEntries)
	setDefault(&cfg.Memory.MaxBytes, defaultMemoryMaxBytes)
	setDefault(&cfg.Memory.CleanupInterval, defaultCleanupInterval)
}

func setDefault[T comparable](v *T, def T) {
	var zero T
	if *v == zero {
		*v = def
	}
}

// ValidateConfig checks cfg after applying the defaults to a copy. A
// disabled config is always valid; only the selected driver's section is
// checked, and nothing is dialed or read from disk.
func ValidateConfig(cfg Config) error {
	if !cfg.Enabled {
		return nil
	}
	cfg = cfg.clone()
	ApplyDefaults(&cfg)

	switch cfg.Driver {
	case DriverRedis, DriverMemory:
	default:
		return fmt.Errorf("%w: cache.driver must be redis or memory, got %q", ErrUnsupportedDriver, cfg.Driver)
	}
	switch cfg.Codec {
	case CodecJSON, CodecCBOR:
	default:
		return fmt.Errorf("%w: cache.codec must be json or cbor, got %q", ErrUnsupportedCodec, cfg.Codec)
	}
	l := cfg.Limits
	if l.MaxKeyBytes < 0 || l.MaxKeyBytes > maxKeyBytesLimit {
		return invalid("cache.limits.max_key_bytes must be between 1 and %d", maxKeyBytesLimit)
	}
	if l.MaxValueBytes < 0 || l.MaxValueBytes > maxValueBytesLimit {
		return invalid("cache.limits.max_value_bytes must be between 1 and %d", int64(maxValueBytesLimit))
	}
	if l.MaxInflightLoads < 0 {
		return invalid("cache.limits.max_inflight_loads must be positive")
	}
	if cfg.LoadTimeout < 0 {
		return invalid("cache.load_timeout must be positive")
	}
	if cfg.Prefix != "" {
		if hasControl(cfg.Prefix) {
			return invalid("cache.prefix must not contain control characters")
		}
		if len(cfg.Prefix)+1 >= l.MaxKeyBytes {
			return invalid("cache.prefix must be shorter than cache.limits.max_key_bytes")
		}
	}
	switch cfg.Driver {
	case DriverRedis:
		return validateRedis(cfg.Redis)
	default:
		return validateMemory(cfg)
	}
}

func validateMemory(cfg Config) error {
	m := cfg.Memory
	if m.MaxEntries < 0 {
		return invalid("cache.memory.max_entries must be positive")
	}
	if m.MaxBytes < 0 {
		return invalid("cache.memory.max_bytes must be positive")
	}
	if int64(m.MaxBytes) < int64(cfg.Limits.MaxKeyBytes)+int64(cfg.Limits.MaxValueBytes) {
		return invalid("cache.memory.max_bytes must hold at least one entry of max_key_bytes + max_value_bytes")
	}
	if m.CleanupInterval < 0 {
		return invalid("cache.memory.cleanup_interval must be positive")
	}
	return nil
}

func validateRedis(r RedisConfig) error {
	if r.URL != "" {
		u, err := parseRedisURL(r.URL)
		if err != nil {
			return err
		}
		if r.TLS.Enabled && !u.tls {
			return invalid("cache.redis.tls.enabled conflicts with a redis:// url; use rediss://")
		}
	} else {
		if r.Host == "" || hasControl(r.Host) || strings.ContainsAny(r.Host, "/ ") {
			return invalid("cache.redis.host is invalid")
		}
		if r.Port < 1 || r.Port > 65535 {
			return invalid("cache.redis.port must be between 1 and 65535")
		}
		if r.Database < 0 {
			return invalid("cache.redis.database must not be negative")
		}
	}
	if hasControl(r.Username) || hasControl(r.Password) {
		return invalid("cache.redis credentials must not contain control characters")
	}
	if hasControl(r.TLS.ServerName) || strings.ContainsAny(r.TLS.ServerName, "/ ") {
		return invalid("cache.redis.tls.server_name is invalid")
	}
	if hasControl(r.TLS.CAFile) {
		return invalid("cache.redis.tls.ca_file is invalid")
	}

	p := r.Pool
	if p.Size < 0 || p.MinIdleConns < 0 || p.MaxIdleConns < 0 || p.MaxActiveConns < 0 ||
		p.MaxConnIdleTime < 0 || p.MaxConnLifetime < 0 {
		return invalid("cache.redis.pool values must not be negative")
	}
	if p.MinIdleConns > p.MaxIdleConns {
		return invalid("cache.redis.pool.min_idle_conns must not exceed max_idle_conns")
	}
	if p.MaxIdleConns > p.Size {
		return invalid("cache.redis.pool.max_idle_conns must not exceed size")
	}
	if p.Size > p.MaxActiveConns {
		return invalid("cache.redis.pool.size must not exceed max_active_conns")
	}
	t := r.Timeouts
	if t.Dial < 0 || t.Read < 0 || t.Write < 0 || t.Pool < 0 || t.Operation < 0 {
		return invalid("cache.redis.timeouts must not be negative")
	}
	if n := *r.Retry.ReadRetries; n < 0 || n > maxReadRetries {
		return invalid("cache.redis.retry.read_retries must be between 0 and %d", maxReadRetries)
	}
	if r.Retry.MinBackoff < 0 || r.Retry.MaxBackoff < 0 {
		return invalid("cache.redis.retry backoffs must not be negative")
	}
	if r.Retry.MinBackoff > r.Retry.MaxBackoff {
		return invalid("cache.redis.retry.min_backoff must not exceed max_backoff")
	}
	return nil
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrInvalidConfig}, args...)...)
}

func hasControl(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 || c == 0x7f {
			return true
		}
	}
	return false
}

// redisURL is the parsed form of cache.redis.url.
type redisURL struct {
	host     string
	port     int
	username string
	password string
	hasAuth  bool
	database int
	tls      bool
}

// parseRedisURL accepts redis://[user[:password]@]host[:port][/db] and the
// rediss:// form. Query options are rejected so that pool and timeout
// behavior has a single source (the structured fields). Error messages
// never echo the URL, which may carry credentials.
func parseRedisURL(raw string) (redisURL, error) {
	var out redisURL
	if hasControl(raw) {
		return out, invalid("cache.redis.url must not contain control characters")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return out, invalid("cache.redis.url is not a valid URL")
	}
	switch u.Scheme {
	case "redis":
	case "rediss":
		out.tls = true
	default:
		return out, invalid("cache.redis.url scheme must be redis or rediss")
	}
	if u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return out, invalid("cache.redis.url must not have query options or a fragment; use the structured fields")
	}
	out.host = u.Hostname()
	if out.host == "" {
		return out, invalid("cache.redis.url must have a host")
	}
	out.port = defaultRedisPort
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return out, invalid("cache.redis.url port must be between 1 and 65535")
		}
		out.port = n
	} else if strings.HasSuffix(u.Host, ":") {
		return out, invalid("cache.redis.url port must be between 1 and 65535")
	}
	if path := strings.TrimPrefix(u.Path, "/"); path != "" {
		n, err := strconv.Atoi(path)
		if err != nil || n < 0 || strings.ContainsAny(path, "+-") {
			return out, invalid("cache.redis.url database must be a non-negative integer")
		}
		out.database = n
	}
	if u.User != nil {
		out.hasAuth = true
		out.username = u.User.Username()
		out.password, _ = u.User.Password()
	}
	return out, nil
}

// endpoint resolves the connection target: the URL wins over the
// structured fields.
func (r RedisConfig) endpoint() (redisURL, error) {
	if r.URL != "" {
		return parseRedisURL(r.URL)
	}
	return redisURL{
		host: r.Host, port: r.Port, username: r.Username, password: r.Password,
		hasAuth: r.Username != "" || r.Password != "", database: r.Database, tls: r.TLS.Enabled,
	}, nil
}

func (u redisURL) addr() string { return net.JoinHostPort(u.host, strconv.Itoa(u.port)) }

// clone deep-copies the only reference field (read_retries).
func (cfg Config) clone() Config {
	if p := cfg.Redis.Retry.ReadRetries; p != nil {
		n := *p
		cfg.Redis.Retry.ReadRetries = &n
	}
	return cfg
}

const redacted = "[REDACTED]"

func (r RedisConfig) redacted() RedisConfig {
	if r.URL != "" {
		r.URL = redacted
	}
	if r.Password != "" {
		r.Password = redacted
	}
	if r.TLS.CAFile != "" {
		r.TLS.CAFile = redacted
	}
	if p := r.Retry.ReadRetries; p != nil { // print the value, not the address
		n := *p
		r.Retry.ReadRetries = &n
	}
	return r
}

// Format redacts the URL, password and CA file path for every verb (%v,
// %+v, %#v, %s), including when RedisConfig is printed inside Config.
func (r RedisConfig) Format(f fmt.State, verb rune) {
	type plain RedisConfig
	_, _ = fmt.Fprintf(f, fmt.FormatString(f, verb), plain(r.redacted()))
}

// LogValue reports the shape of the configuration for slog, without
// secrets, paths or addresses.
func (cfg Config) LogValue() slog.Value {
	attrs := []slog.Attr{
		slog.Bool("enabled", cfg.Enabled),
		slog.String("driver", cfg.Driver),
		slog.String("codec", cfg.Codec),
		slog.Bool("custom_prefix", cfg.Prefix != ""),
	}
	switch cfg.Driver {
	case DriverRedis:
		attrs = append(attrs, slog.Group("redis",
			slog.Bool("url", cfg.Redis.URL != ""),
			slog.Bool("tls", cfg.Redis.TLS.Enabled || strings.HasPrefix(cfg.Redis.URL, "rediss:")),
			slog.Int("pool_size", cfg.Redis.Pool.Size),
		))
	case DriverMemory:
		attrs = append(attrs, slog.Group("memory",
			slog.Int("max_entries", cfg.Memory.MaxEntries),
			slog.Int64("max_bytes", int64(cfg.Memory.MaxBytes)),
		))
	}
	return slog.GroupValue(attrs...)
}

// ByteSize is a size in bytes that accepts units in YAML and env files:
// "1mb", "512kb", "1.5gb", "100b" or a plain number of bytes. Units are
// base 1024 and case-insensitive ("5MB", "5 MiB"), the same syntax as
// config.ByteSize (the cache package cannot import config).
type ByteSize int64

// Size units.
const (
	B  ByteSize = 1
	KB          = 1024 * B
	MB          = 1024 * KB
	GB          = 1024 * MB
)

var byteUnits = map[string]ByteSize{
	"": B, "b": B,
	"k": KB, "kb": KB, "kib": KB,
	"m": MB, "mb": MB, "mib": MB,
	"g": GB, "gb": GB, "gib": GB,
}

// ParseByteSize parses a size such as "1mb" or "1048576".
func ParseByteSize(s string) (ByteSize, error) {
	str := strings.ToLower(strings.TrimSpace(s))
	i := strings.IndexFunc(str, func(r rune) bool { return (r < '0' || r > '9') && r != '.' })
	if i < 0 {
		i = len(str)
	}
	num, unit := str[:i], strings.TrimSpace(str[i:])
	mult, ok := byteUnits[unit]
	if num == "" || !ok {
		return 0, fmt.Errorf("invalid size %q (use e.g. 1mb, 512kb, 1048576)", s)
	}
	n, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	v := n * float64(mult)
	if v >= math.MaxInt64 {
		return 0, fmt.Errorf("invalid size %q: too large", s)
	}
	return ByteSize(v), nil
}

// UnmarshalText implements encoding.TextUnmarshaler (env files).
func (b *ByteSize) UnmarshalText(text []byte) error {
	v, err := ParseByteSize(string(text))
	if err != nil {
		return err
	}
	*b = v
	return nil
}

// UnmarshalYAML accepts both "1mb" and 1048576 in YAML. It uses the
// unmarshal-function form so the package needs no YAML dependency.
func (b *ByteSize) UnmarshalYAML(unmarshal func(any) error) error {
	var s string
	if err := unmarshal(&s); err != nil {
		return errors.New("invalid size: expected a string or a number")
	}
	return b.UnmarshalText([]byte(s))
}
