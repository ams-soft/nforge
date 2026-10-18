package config

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ams-soft/nforge/cache"
	"github.com/ams-soft/nforge/jose"
	"gopkg.in/yaml.v3"
)

const defaultConfigFileName = "config.yaml"

var hexColorRe = regexp.MustCompile(`^#([0-9a-fA-F]{3,4}|[0-9a-fA-F]{6}|[0-9a-fA-F]{8})$`)

func Load() (*Config, error) {
	configPath := flag.String("config", "", "path to the base config.yaml")
	envFilePath := flag.String("envfile", "", "path to the env overrides file")
	flag.Parse()

	return LoadFlags(*configPath, *envFilePath)
}

func LoadFlags(configPath, envFilePath string) (*Config, error) {
	cfg := &Config{Env: "local"}

	basePath := configPath
	if basePath == "" {
		root, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("resolve working directory: %w", err)
		}
		basePath = filepath.Join(root, defaultConfigFileName)
	}

	if err := loadYAMLFile(basePath, cfg); err != nil {
		return nil, fmt.Errorf("load base config %q: %w", basePath, err)
	}

	if envFilePath != "" {
		if err := applyEnvFile(envFilePath, cfg); err != nil {
			return nil, fmt.Errorf("apply env file %q: %w", envFilePath, err)
		}
	}

	if overlay := overlayFileFor(cfg.Env); overlay != "" {
		if _, err := os.Stat(overlay); err == nil {
			if err := loadYAMLFile(overlay, cfg); err != nil {
				return nil, fmt.Errorf("load overlay %q: %w", overlay, err)
			}
		}
	}

	cfg = feedDefaults(cfg)

	if valid, err := validateRequiredConfig(cfg); !valid {
		return nil, fmt.Errorf("validate config: %w", err)
	}

	return cfg, nil
}

func feedDefaults(cfg *Config) *Config {
	if len(cfg.Codec.Formats.Enabled) == 0 {
		cfg.Codec.Formats.Enabled = []string{"json"}
	}
	if cfg.Codec.MaxPayloadBytes == 0 {
		cfg.Codec.MaxPayloadBytes = 5 * MB
	}
	cfg.App.OnlineSince = time.Now().Format(time.RFC3339)
	if cfg.Codec.Default == "" {
		cfg.Codec.Default = "json"
	}
	if cfg.Codec.XML.DisallowDTD == nil {
		disallow := true
		cfg.Codec.XML.DisallowDTD = &disallow
	}
	feedRoutesDefaults(cfg)
	feedDocsDefaults(cfg)
	if cfg.HTTP.Auth.Realm == "" {
		cfg.HTTP.Auth.Realm = "nforge-api"
	}
	if cfg.HTTP.Tenant.Source == "" {
		cfg.HTTP.Tenant.Source = "claim"
	}
	if cfg.HTTP.Tenant.Header == "" {
		cfg.HTTP.Tenant.Header = "X-Tenant-ID"
	}
	feedSecurityDefaults(&cfg.HTTP.Security)
	feedCORSDefaults(&cfg.HTTP.CORS)
	jose.ApplyDefaults(&cfg.JOSE)
	cache.ApplyDefaults(&cfg.Cache)
	return cfg
}

func feedRoutesDefaults(cfg *Config) {
	if cfg.HTTP.Routes.Health.Enabled {
		if cfg.HTTP.Routes.Health.Path == "" {
			cfg.HTTP.Routes.Health.Path = "/health"
		}
	}
	if cfg.HTTP.Routes.Readiness.Enabled {
		if cfg.HTTP.Routes.Readiness.Path == "" {
			cfg.HTTP.Routes.Readiness.Path = "/ready"
		}
	}
}

func feedDocsDefaults(cfg *Config) {
	docs := &cfg.HTTP.Docs
	if docs.Path == "" {
		docs.Path = "/docs"
	}
	docs.Path = "/" + strings.Trim(docs.Path, "/")
	if docs.Title == "" {
		docs.Title = cfg.App.Name
	}
	if docs.Version == "" {
		docs.Version = cfg.App.Version
	}

	if docs.Auth == "" {
		docs.Auth = "bearer"
	}
	if docs.Accent == "" {
		docs.Accent = "#007d9cbf"
	}
	if len(docs.Hosts) == 0 {
		host := cfg.HTTP.Server.Host
		if host == "" || host == "0.0.0.0" || host == "::" {
			host = "localhost"
		}
		docs.Hosts = []string{"http://" + net.JoinHostPort(host, strconv.Itoa(cfg.HTTP.Server.Port))}
	}
}

func validateRequiredConfig(cfg *Config) (bool, error) {
	if cfg.App.Name == "" {
		return false, fmt.Errorf("app.name must not be empty")
	}
	if cfg.App.Version == "" {
		return false, fmt.Errorf("app.version must not be empty")
	}
	if cfg.HTTP.Server.Port <= 0 || cfg.HTTP.Server.Port > 65535 {
		return false, fmt.Errorf("http.server.port must be between 1 and 65535")
	}
	if auth := cfg.HTTP.Docs.Auth; auth != "bearer" && auth != "none" {
		return false, fmt.Errorf("http.docs.auth must be bearer or none, got %q", auth)
	}
	if accent := cfg.HTTP.Docs.Accent; !hexColorRe.MatchString(accent) {
		return false, fmt.Errorf("http.docs.accent must be a hex color (#RGB, #RGBA, #RRGGBB or #RRGGBBAA), got %q", accent)
	}
	if err := jose.ValidateConfig(cfg.JOSE); err != nil {
		return false, err
	}
	if err := validateAuth(cfg); err != nil {
		return false, err
	}
	if err := validateTenant(cfg); err != nil {
		return false, err
	}
	if err := validateCache(cfg); err != nil {
		return false, err
	}
	if err := validateSecurity(cfg.HTTP.Security); err != nil {
		return false, err
	}
	if err := validateCORS(cfg.HTTP.CORS); err != nil {
		return false, err
	}
	return true, nil
}

// validateCache checks the cache section and, when it is enabled, that its
// object codec is one of codec.formats.enabled (the cache serializes through
// the same registry, so a disabled format would fail at startup anyway).
func validateCache(cfg *Config) error {
	if !cfg.Cache.Enabled {
		return nil
	}
	if err := cache.ValidateConfig(cfg.Cache); err != nil {
		return err
	}
	for _, f := range cfg.Codec.Formats.Enabled {
		if strings.EqualFold(strings.TrimSpace(f), cfg.Cache.Codec) {
			return nil
		}
	}
	return fmt.Errorf("%w: cache.codec %q must be listed in codec.formats.enabled", cache.ErrUnsupportedCodec, cfg.Cache.Codec)
}

var headerNameRe = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")

// validateTenant checks http.tenant and, when the database is enabled,
// database.tenancy (so a bad tenancy section fails at load, before dialing).
func validateTenant(cfg *Config) error {
	switch cfg.HTTP.Tenant.Source {
	case "claim", "header":
	default:
		return fmt.Errorf("http.tenant.source must be claim or header, got %q", cfg.HTTP.Tenant.Source)
	}
	if !headerNameRe.MatchString(cfg.HTTP.Tenant.Header) {
		return fmt.Errorf("http.tenant.header %q is not a valid header name", cfg.HTTP.Tenant.Header)
	}
	if cfg.Database.Enabled {
		return cfg.Database.Tenancy.Validate()
	}
	return nil
}

// validateAuth checks http.auth against the jose profiles. The realm goes
// into a quoted-string of WWW-Authenticate, so quotes, backslashes and
// control characters are rejected.
func validateAuth(cfg *Config) error {
	realm := cfg.HTTP.Auth.Realm
	if len(realm) > 128 {
		return fmt.Errorf("http.auth.realm must have at most 128 characters")
	}
	for i := 0; i < len(realm); i++ {
		if c := realm[i]; c < 0x20 || c > 0x7e || c == '"' || c == '\\' {
			return fmt.Errorf("http.auth.realm must be printable ASCII without quotes or backslashes")
		}
	}
	name := cfg.HTTP.Auth.DefaultProfile
	if name == "" {
		return nil
	}
	if !cfg.JOSE.Enabled {
		return fmt.Errorf("http.auth.default_profile requires jose.enabled: true")
	}
	for _, p := range cfg.JOSE.Profiles {
		if p.Name == name {
			if p.Kind != jose.KindJWTSigned && p.Kind != jose.KindJWTNested {
				return fmt.Errorf("http.auth.default_profile %q must be a jwt_signed or jwt_nested profile", name)
			}
			if len(p.VerificationKeys) == 0 {
				return fmt.Errorf("http.auth.default_profile %q cannot validate tokens", name)
			}
			return nil
		}
	}
	return fmt.Errorf("http.auth.default_profile %q is not a configured jose profile", name)
}

func loadYAMLFile(path string, cfg *Config) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return err
	}
	expandEnvNodes(&root)
	if err := checkJOSEKeys(&root); err != nil {
		return err
	}
	return root.Decode(cfg)
}

// checkJOSEKeys rejects unknown keys inside the `jose` section only (a typo
// there could silently disable a security setting); the rest of the file
// keeps the permissive decoding.
func checkJOSEKeys(root *yaml.Node) error {
	doc := root
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		doc = doc.Content[0]
	}
	if doc.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(doc.Content); i += 2 {
		if doc.Content[i].Value != "jose" {
			continue
		}
		data, err := yaml.Marshal(doc.Content[i+1])
		if err != nil {
			return fmt.Errorf("jose: %w", err)
		}
		dec := yaml.NewDecoder(bytes.NewReader(data))
		dec.KnownFields(true)
		var probe jose.Config
		if err := dec.Decode(&probe); err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("jose: %w", err)
		}
	}
	return nil
}

var envRefRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// expandEnvNodes replaces ${VAR} with the environment variable's value in all
// YAML scalars; undefined variables become an empty string. Expansion is done
// per node (not on the raw text) so that values with special YAML characters
// do not break parsing.
func expandEnvNodes(n *yaml.Node) {
	if n.Kind == yaml.ScalarNode && strings.Contains(n.Value, "${") {
		n.Value = envRefRe.ReplaceAllStringFunc(n.Value, func(ref string) string {
			return os.Getenv(envRefRe.FindStringSubmatch(ref)[1])
		})
		if n.Style&(yaml.SingleQuotedStyle|yaml.DoubleQuotedStyle) == 0 {
			n.Tag = "" // re-resolve: `port: ${DB_PORT}` must become int, not !!str
		}
		return
	}
	for _, c := range n.Content {
		expandEnvNodes(c)
	}
}

func overlayFileFor(env string) string {
	switch strings.ToUpper(env) {
	case "PRD":
		return "config.prd.yaml"
	case "STG":
		return "config.stg.yaml"
	case "DEV":
		return "config.dev.yaml"
	default:
		return ""
	}
}

func (cfg *Config) IsLoggingEnabled() bool {
	return cfg.Logging.Output.Terminal.Enabled || cfg.Logging.Output.Otel.Enabled
}

// IsTracingEnabled reports whether spans are exported: OTel must be on
// (logging.output.otel.enabled) and tracing.otel.enabled, which defaults to
// true, must not be false.
func (cfg *Config) IsTracingEnabled() bool {
	if !cfg.Logging.Output.Otel.Enabled {
		return false
	}
	return cfg.Tracing.Otel.Enabled == nil || *cfg.Tracing.Otel.Enabled
}
