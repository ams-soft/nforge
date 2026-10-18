package config

import (
	"time"

	"github.com/ams-soft/nforge/cache"
	"github.com/ams-soft/nforge/database"
	"github.com/ams-soft/nforge/jose"
)

type Config struct {
	Env      string          `yaml:"env"` // local (default) | dev | stg | prd
	App      AppConfig       `yaml:"app"`
	HTTP     HTTPConfig      `yaml:"http"`
	Database database.Config `yaml:"database"`
	Logging  LoggingConfig   `yaml:"logging"`
	Tracing  TracingConfig   `yaml:"tracing"`
	Codec    CodecConfig     `yaml:"codec"`
	JOSE     jose.Config     `yaml:"jose"`  // JWS/JWE/JWT; disabled by default (deps.JOSE is nil)
	Cache    cache.Config    `yaml:"cache"` // Redis or memory cache; disabled by default (deps.Cache is nil)
}

type AppConfig struct {
	Name        string `yaml:"name"`
	Version     string `yaml:"version"`
	OnlineSince string
}

type HealthConfig struct {
	Enabled bool   `yaml:"enabled"`
	Path    string `yaml:"path"`
}
type ReadinessConfig struct {
	Enabled bool   `yaml:"enabled"`
	Path    string `yaml:"path"`
}
type RoutesConfig struct {
	Health    HealthConfig    `yaml:"health"`
	Readiness ReadinessConfig `yaml:"readiness"`
}

type HTTPConfig struct {
	Server ServerConfig `yaml:"server"`
	Docs   DocsConfig   `yaml:"docs"`
	Routes RoutesConfig `yaml:"routes"`
	Auth   AuthConfig   `yaml:"auth"`
	Tenant TenantConfig `yaml:"tenant"`
	// Security and CORS are go-ams-router middlewares applied to every
	// request, right after the Recoverer; both are disabled by default.
	Security SecurityConfig `yaml:"security"`
	CORS     CORSConfig     `yaml:"cors"`
}

// SecurityConfig enables go-ams-router's middleware.Security: the security
// response headers of helmet with its defaults (CSP, HSTS, X-Frame-Options,
// Referrer-Policy, Cross-Origin-*, nosniff…). Only what differs from the
// defaults needs to be set. Values are validated at load.
type SecurityConfig struct {
	Enabled bool `yaml:"enabled"` // default: false
	// Disable lists response headers not to send, case-insensitive (e.g.
	// [x-frame-options, origin-agent-cluster]); default: none.
	Disable                   []string   `yaml:"disable"`
	CSP                       CSPConfig  `yaml:"csp"`
	HSTS                      HSTSConfig `yaml:"hsts"`
	FrameOptions              string     `yaml:"frame_options"`                // SAMEORIGIN (default) | DENY
	ReferrerPolicy            []string   `yaml:"referrer_policy"`              // default: [no-referrer]; several = fallbacks, the browser uses the last it knows
	CrossOriginOpenerPolicy   string     `yaml:"cross_origin_opener_policy"`   // same-origin (default) | same-origin-allow-popups | unsafe-none
	CrossOriginResourcePolicy string     `yaml:"cross_origin_resource_policy"` // same-origin (default) | same-site | cross-origin
	CrossOriginEmbedderPolicy string     `yaml:"cross_origin_embedder_policy"` // "" (not sent, default) | require-corp | credentialless | unsafe-none
}

// CSPConfig shapes the Content-Security-Policy header. The docs page
// (http.docs) always gets its own policy, which allows what it loads.
type CSPConfig struct {
	// Directives are merged over helmet's defaults (default-src 'self', …).
	// Keys are kebab-case ("script-src"); a null value removes a default,
	// [] emits a directive without value (upgrade-insecure-requests).
	// Keywords must be quoted: "'self'". YAML only (not from the env file).
	Directives      map[string][]string `yaml:"directives"`
	DisableDefaults bool                `yaml:"disable_defaults"` // start from no directive at all; default-src is then required
	ReportOnly      bool                `yaml:"report_only"`      // send Content-Security-Policy-Report-Only instead
}

// HSTSConfig shapes the Strict-Transport-Security header (browsers ignore it
// on plain http).
type HSTSConfig struct {
	MaxAge            time.Duration `yaml:"max_age"`            // default: 8760h (365 days); whole seconds
	IncludeSubdomains *bool         `yaml:"include_subdomains"` // default: true
	Preload           bool          `yaml:"preload"`            // default: false
}

// CORSConfig enables go-ams-router's middleware.CORS (the Express cors
// package semantics). It runs before route matching, so preflights are
// answered (204) for every path, private routes included.
type CORSConfig struct {
	Enabled bool `yaml:"enabled"` // default: false
	// Origins are exact origins ("https://app.example.com"); empty or ["*"]
	// allows any origin with Access-Control-Allow-Origin: *.
	Origins []string `yaml:"origins"`
	// OriginPatterns are regexps allowed in addition to Origins; they must be
	// anchored (^…$) so "https://example.com.evil.net" cannot match.
	OriginPatterns       []string      `yaml:"origin_patterns"`
	Methods              []string      `yaml:"methods"`                // default: GET,HEAD,PUT,PATCH,POST,DELETE
	AllowedHeaders       []string      `yaml:"allowed_headers"`        // default: echoes the preflight's Access-Control-Request-Headers
	ExposedHeaders       []string      `yaml:"exposed_headers"`        // default: none (e.g. [X-Request-Id])
	Credentials          bool          `yaml:"credentials"`            // default: false; requires explicit origins
	MaxAge               time.Duration `yaml:"max_age"`                // preflight cache, whole seconds; default: 0 (header not sent)
	OptionsSuccessStatus int           `yaml:"options_success_status"` // 200..299; default: 204
}

// TenantConfig selects where the tenant middleware of typed routes
// (Opts.Tenant) and nhttp.Tenant() read the tenant ID from.
type TenantConfig struct {
	Source string `yaml:"source"` // claim (Principal.TenantID from the JWT, default) | header
	Header string `yaml:"header"` // header read when source is header; default: "X-Tenant-ID"
}

// AuthConfig configures the bearer authentication of private typed routes
// (Opts.IsPrivate). It requires jose.enabled.
type AuthConfig struct {
	DefaultProfile string `yaml:"default_profile"` // jwt profile used by private routes without Opts.AuthProfile; default: none
	Realm          string `yaml:"realm"`           // WWW-Authenticate realm; default: "nforge-api"
}

// DocsConfig configures the OpenAPI documentation generated from the typed routes.
type DocsConfig struct {
	Enabled bool     `yaml:"enabled"` // default: false
	Path    string   `yaml:"path"`    // default: "/docs" (spec at {path}/swagger.json and .yaml)
	Title   string   `yaml:"title"`   // default: app.name
	Version string   `yaml:"version"` // default: app.version
	Auth    string   `yaml:"auth"`    // documented scheme for IsPrivate routes: bearer (default) | none (none is invalid with private typed routes)
	Hosts   []string `yaml:"hosts"`   // default: ["http://{server.host|localhost}:{server.port}"]
	Accent  string   `yaml:"accent"`  // page accent color (hex #RGB[A] or #RRGGBB[AA]); default: "#007d9cbf"
}

type ServerConfig struct {
	Host   string `yaml:"host"`
	Port   int    `yaml:"port"`
	Header bool   `yaml:"header"`
}

type LoggingConfig struct {
	Output LoggingOutput `yaml:"output"`
}

type LoggingOutput struct {
	Otel     OtelOutput     `yaml:"otel"`
	Terminal TerminalOutput `yaml:"terminal"`
}

type OtelOutput struct {
	Enabled  bool   `yaml:"enabled"`
	Endpoint string `yaml:"endpoint"` // "host:port" or URL (http:// implies insecure); default: "localhost:4317"
	Insecure bool   `yaml:"insecure"` // default: false
}

type TerminalOutput struct {
	Enabled bool `yaml:"enabled"`
}

type TracingConfig struct {
	Otel TracingOtel `yaml:"otel"`
}

// TracingOtel only takes effect when logging.output.otel.enabled is true; see
// Config.IsTracingEnabled.
type TracingOtel struct {
	Enabled  *bool  `yaml:"enabled"`  // default: true
	Endpoint string `yaml:"endpoint"` // "host:port" or URL (http:// implies insecure); default: "localhost:4317"
	Insecure bool   `yaml:"insecure"` // default: false
}

type CodecConfig struct {
	Formats             CodecFormats   `yaml:"formats"`
	Default             string         `yaml:"default"`               // default: "json"
	StrictUnknownFields bool           `yaml:"strict_unknown_fields"` // default: false
	MaxPayloadBytes     ByteSize       `yaml:"max_payload_bytes"`     // e.g. 5mb, 512kb, 1048576; default: 5mb
	XML                 CodecXMLConfig `yaml:"xml"`
}

type CodecFormats struct {
	Enabled []string `yaml:"enabled"` // json | cbor | xml; default: [json]
}

type CodecXMLConfig struct {
	DisallowDTD *bool `yaml:"disallow_dtd"` // default: true
}
