package cache

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestApplyDefaults(t *testing.T) {
	var cfg Config
	ApplyDefaults(&cfg)
	if cfg.Driver != DriverRedis || cfg.Codec != CodecJSON {
		t.Fatalf("driver/codec = %q/%q", cfg.Driver, cfg.Codec)
	}
	if cfg.Limits.MaxKeyBytes != 1024 || cfg.Limits.MaxValueBytes != MB || cfg.Limits.MaxInflightLoads != 1024 {
		t.Fatalf("limits = %+v", cfg.Limits)
	}
	if cfg.LoadTimeout != 5*time.Second {
		t.Fatalf("load_timeout = %v", cfg.LoadTimeout)
	}
	r := cfg.Redis
	if r.Host != "localhost" || r.Port != 6379 || r.Database != 0 {
		t.Fatalf("redis address = %s:%d/%d", r.Host, r.Port, r.Database)
	}
	if r.Pool != (RedisPoolConfig{32, 2, 32, 32, 5 * time.Minute, 30 * time.Minute}) {
		t.Fatalf("pool = %+v", r.Pool)
	}
	if r.Timeouts != (RedisTimeouts{time.Second, time.Second, time.Second, 500 * time.Millisecond, 2 * time.Second}) {
		t.Fatalf("timeouts = %+v", r.Timeouts)
	}
	if *r.Retry.ReadRetries != 1 || r.Retry.MinBackoff != 50*time.Millisecond || r.Retry.MaxBackoff != 500*time.Millisecond {
		t.Fatalf("retry = %+v", r.Retry)
	}
	if cfg.Memory.MaxEntries != 10000 || cfg.Memory.MaxBytes != 64*MB || cfg.Memory.CleanupInterval != 30*time.Second {
		t.Fatalf("memory = %+v", cfg.Memory)
	}
	if r.Username != "" || r.Password != "" || r.URL != "" || cfg.Prefix != "" {
		t.Fatal("optional fields must stay empty")
	}
}

func TestExplicitZeroReadRetriesIsKept(t *testing.T) {
	zero := 0
	cfg := Config{Redis: RedisConfig{Retry: RedisRetry{ReadRetries: &zero}}}
	ApplyDefaults(&cfg)
	if *cfg.Redis.Retry.ReadRetries != 0 {
		t.Fatalf("read_retries = %d, want 0", *cfg.Redis.Retry.ReadRetries)
	}
}

func TestValidateConfig(t *testing.T) {
	neg := -1
	four := 4
	cases := []struct {
		name   string
		mutate func(*Config)
		want   error
	}{
		{"disabled is always valid", func(c *Config) { c.Enabled = false; c.Driver = "nope" }, nil},
		{"redis defaults", func(c *Config) {}, nil},
		{"memory defaults", func(c *Config) { c.Driver = "memory" }, nil},
		{"uppercase driver", func(c *Config) { c.Driver = "MEMORY" }, nil},
		{"unknown driver", func(c *Config) { c.Driver = "memcached" }, ErrUnsupportedDriver},
		{"unknown codec", func(c *Config) { c.Codec = "xml" }, ErrUnsupportedCodec},
		{"cbor codec", func(c *Config) { c.Codec = "cbor" }, nil},
		{"negative key limit", func(c *Config) { c.Limits.MaxKeyBytes = -1 }, ErrInvalidConfig},
		{"key limit too large", func(c *Config) { c.Limits.MaxKeyBytes = 64*1024 + 1 }, ErrInvalidConfig},
		{"value limit too large", func(c *Config) { c.Limits.MaxValueBytes = 64*MB + 1 }, ErrInvalidConfig},
		{"negative inflight", func(c *Config) { c.Limits.MaxInflightLoads = -1 }, ErrInvalidConfig},
		{"negative load timeout", func(c *Config) { c.LoadTimeout = -time.Second }, ErrInvalidConfig},
		{"prefix with control", func(c *Config) { c.Prefix = "a\nb" }, ErrInvalidConfig},
		{"invalid port", func(c *Config) { c.Redis.Port = 70000 }, ErrInvalidConfig},
		{"negative port", func(c *Config) { c.Redis.Port = -1 }, ErrInvalidConfig},
		{"negative database", func(c *Config) { c.Redis.Database = -1 }, ErrInvalidConfig},
		{"min idle > max idle", func(c *Config) { c.Redis.Pool.MinIdleConns = 33 }, ErrInvalidConfig},
		{"max idle > size", func(c *Config) { c.Redis.Pool.Size = 8; c.Redis.Pool.MinIdleConns = 1 }, ErrInvalidConfig},
		{"size > max active", func(c *Config) { c.Redis.Pool.MaxActiveConns = 8 }, ErrInvalidConfig},
		{"negative timeout", func(c *Config) { c.Redis.Timeouts.Read = -1 }, ErrInvalidConfig},
		{"negative retries", func(c *Config) { c.Redis.Retry.ReadRetries = &neg }, ErrInvalidConfig},
		{"too many retries", func(c *Config) { c.Redis.Retry.ReadRetries = &four }, ErrInvalidConfig},
		{"min backoff > max", func(c *Config) { c.Redis.Retry.MinBackoff = time.Second }, ErrInvalidConfig},
		{"tls with redis url", func(c *Config) { c.Redis.URL = "redis://h:1"; c.Redis.TLS.Enabled = true }, ErrInvalidConfig},
		{"username without password", func(c *Config) { c.Redis.Username = "app" }, nil},
		{"memory negative entries", func(c *Config) { c.Driver = "memory"; c.Memory.MaxEntries = -1 }, ErrInvalidConfig},
		{"memory too small", func(c *Config) { c.Driver = "memory"; c.Memory.MaxBytes = MB }, ErrInvalidConfig},
		{"memory ignores bad redis", func(c *Config) { c.Driver = "memory"; c.Redis.Port = -5 }, nil},
		{"redis ignores bad memory", func(c *Config) { c.Memory.MaxEntries = -5 }, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{Enabled: true}
			tc.mutate(&cfg)
			err := ValidateConfig(cfg)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			mustIs(t, err, tc.want)
		})
	}
}

func TestParseRedisURL(t *testing.T) {
	cases := []struct {
		url  string
		ok   bool
		want redisURL
	}{
		{"redis://localhost", true, redisURL{host: "localhost", port: 6379}},
		{"redis://h:6380/3", true, redisURL{host: "h", port: 6380, database: 3}},
		{"rediss://user:secret@h", true, redisURL{host: "h", port: 6379, username: "user", password: "secret", hasAuth: true, tls: true}},
		{"redis://:secret@h", true, redisURL{host: "h", port: 6379, password: "secret", hasAuth: true}},
		{"redis://[::1]:7000", true, redisURL{host: "::1", port: 7000}},
		{"http://h", false, redisURL{}},
		{"unix:///tmp/redis.sock", false, redisURL{}},
		{"redis://", false, redisURL{}},
		{"redis://h:0", false, redisURL{}},
		{"redis://h:99999", false, redisURL{}},
		{"redis://h:", false, redisURL{}},
		{"redis://h/-1", false, redisURL{}},
		{"redis://h/x", false, redisURL{}},
		{"redis://h/1?dial_timeout=1s", false, redisURL{}},
		{"redis://h/1#frag", false, redisURL{}},
		{"redis://h\n", false, redisURL{}},
	}
	for _, tc := range cases {
		got, err := parseRedisURL(tc.url)
		if tc.ok != (err == nil) {
			t.Errorf("%q: err = %v", tc.url, err)
			continue
		}
		if err != nil {
			mustIs(t, err, ErrInvalidConfig)
			if strings.Contains(err.Error(), "secret") {
				t.Errorf("error leaks the URL: %v", err)
			}
			continue
		}
		if got != tc.want {
			t.Errorf("%q: got %+v, want %+v", tc.url, got, tc.want)
		}
	}
}

func TestURLWinsOverStructuredFields(t *testing.T) {
	cfg := Config{Enabled: true, Redis: RedisConfig{
		URL: "rediss://u:p@from-url:7000/2", Host: "structured", Port: 1, Username: "x", Password: "y", Database: 9,
	}}
	ApplyDefaults(&cfg)
	ep, err := cfg.Redis.endpoint()
	if err != nil {
		t.Fatal(err)
	}
	want := redisURL{host: "from-url", port: 7000, username: "u", password: "p", hasAuth: true, database: 2, tls: true}
	if ep != want {
		t.Fatalf("endpoint = %+v", ep)
	}
	// URL credentials never fall back to the structured ones
	cfg.Redis.URL = "redis://from-url"
	ep, _ = cfg.Redis.endpoint()
	if ep.username != "" || ep.password != "" || ep.hasAuth {
		t.Fatalf("credentials must come only from the URL: %+v", ep)
	}
}

func TestRedisTLSConfig(t *testing.T) {
	tc, err := redisTLS(RedisTLSConfig{Enabled: true}, "redis.internal")
	if err != nil {
		t.Fatal(err)
	}
	if tc.ServerName != "redis.internal" || tc.MinVersion < 0x0303 || tc.InsecureSkipVerify || tc.RootCAs != nil {
		t.Fatalf("tls = %+v", tc)
	}
	tc, _ = redisTLS(RedisTLSConfig{ServerName: "other"}, "h")
	if tc.ServerName != "other" {
		t.Fatalf("server name = %q", tc.ServerName)
	}

	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.pem")
	_ = os.WriteFile(bad, []byte("not a certificate"), 0o600)
	_, err = redisTLS(RedisTLSConfig{CAFile: bad}, "h")
	mustIs(t, err, ErrInvalidConfig)
	if strings.Contains(err.Error(), dir) {
		t.Fatalf("error leaks the CA path: %v", err)
	}
	_, err = redisTLS(RedisTLSConfig{CAFile: filepath.Join(dir, "missing.pem")}, "h")
	mustIs(t, err, ErrInvalidConfig)
}

func TestCAFileOnlyReadWhenTLSSelected(t *testing.T) {
	// A missing CA file must not matter while TLS is off or the driver is memory.
	cfg := Config{Enabled: true, Driver: DriverMemory, Redis: RedisConfig{TLS: RedisTLSConfig{CAFile: "/does/not/exist"}}}
	c, err := Open(context.Background(), cfg, WithNamespace("e", "a"))
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Shutdown(context.Background())
}

func TestConfigRedaction(t *testing.T) {
	cfg := Config{Enabled: true, Redis: RedisConfig{
		URL: "redis://u:url-secret@h", Password: "pw-secret", TLS: RedisTLSConfig{CAFile: "/secret/ca.pem"},
	}}
	ApplyDefaults(&cfg)
	for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
		for _, v := range []any{cfg, cfg.Redis, &cfg} {
			out := fmt.Sprintf(verb, v)
			for _, secret := range []string{"url-secret", "pw-secret", "/secret/ca.pem"} {
				if strings.Contains(out, secret) {
					t.Fatalf("%s leaks %q: %s", verb, secret, out)
				}
			}
		}
	}
	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("cfg", "cache", cfg)
	if s := buf.String(); strings.Contains(s, "secret") || !strings.Contains(s, `"driver":"redis"`) {
		t.Fatalf("slog output: %s", s)
	}
}

func TestByteSizeYAMLAndText(t *testing.T) {
	var v struct {
		A ByteSize `yaml:"a"`
		B ByteSize `yaml:"b"`
		C ByteSize `yaml:"c"`
	}
	if err := yaml.Unmarshal([]byte("a: 1mb\nb: 1048576\nc: 5 MiB\n"), &v); err != nil {
		t.Fatal(err)
	}
	if v.A != MB || v.B != MB || v.C != 5*MB {
		t.Fatalf("got %+v", v)
	}
	if err := yaml.Unmarshal([]byte("a: lots\n"), &v); err == nil {
		t.Fatal("invalid size must fail")
	}
	var b ByteSize
	if err := b.UnmarshalText([]byte("512kb")); err != nil || b != 512*KB {
		t.Fatalf("text: %v %v", b, err)
	}
	if _, err := ParseByteSize("99999999999gb"); err == nil {
		t.Fatal("overflow must fail")
	}
	if _, err := ParseByteSize("-1mb"); err == nil {
		t.Fatal("negative must fail")
	}
}

func TestOpenDisabledAndInvalid(t *testing.T) {
	if _, err := Open(context.Background(), Config{}); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled: %v", err)
	}
	_, err := Open(context.Background(), Config{Enabled: true, Driver: "x"}, WithNamespace("e", "a"))
	mustIs(t, err, ErrUnsupportedDriver)
	// no namespace and no prefix
	_, err = Open(context.Background(), memoryConfig())
	mustIs(t, err, ErrInvalidConfig)
	// the codec must be enabled in the registry
	cfg := memoryConfig()
	cfg.Codec = CodecCBOR
	reg := testRegistry(t)
	jsonOnly, _ := newJSONOnly()
	_, err = Open(context.Background(), cfg, WithNamespace("e", "a"), WithCodecRegistry(jsonOnly))
	mustIs(t, err, ErrUnsupportedCodec)
	c, err := Open(context.Background(), cfg, WithNamespace("e", "a"), WithCodecRegistry(reg))
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Shutdown(context.Background())
}

func TestOptionsValidation(t *testing.T) {
	reg := testRegistry(t)
	other := testRegistry(t)
	logger := slog.New(slog.DiscardHandler)
	cases := []struct {
		name string
		opts []Option
		ok   bool
	}{
		{"nil option", []Option{nil}, false},
		{"nil registry", []Option{WithCodecRegistry(nil)}, false},
		{"nil logger", []Option{WithLogger(nil)}, false},
		{"nil resolver", []Option{WithTenantResolver(nil)}, false},
		{"empty namespace", []Option{WithNamespace("", "app")}, false},
		{"same registry twice", []Option{WithCodecRegistry(reg), WithCodecRegistry(reg)}, true},
		{"conflicting registries", []Option{WithCodecRegistry(reg), WithCodecRegistry(other)}, false},
		{"same logger twice", []Option{WithLogger(logger), WithLogger(logger)}, true},
		{"conflicting namespaces", []Option{WithNamespace("a", "b"), WithNamespace("a", "c")}, false},
		{"two resolvers", []Option{WithTenantResolver(tenantFrom), WithTenantResolver(tenantFrom)}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := append([]Option{WithNamespace("a", "b")}, tc.opts...)
			c, err := Open(context.Background(), memoryConfig(), opts...)
			if tc.ok {
				if err != nil {
					t.Fatal(err)
				}
				_ = c.Shutdown(context.Background())
				return
			}
			mustIs(t, err, ErrInvalidConfig)
		})
	}
}
