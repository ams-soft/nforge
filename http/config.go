package nhttp

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/ams-soft/nforge/codec"
	"github.com/ams-soft/nforge/module"
)

// Config is the http configuration section, owned by Module.
type Config struct {
	Server ServerConfig `yaml:"server"`
	Docs   DocsConfig   `yaml:"docs"`
	Routes RoutesConfig `yaml:"routes"`
	Auth   AuthConfig   `yaml:"auth"`
	Tenant TenantConfig `yaml:"tenant"`
	// Security and CORS are go-ams-router middlewares applied to every
	// request, right after the Recoverer.
	Security SecurityConfig `yaml:"security"`
	CORS     CORSConfig     `yaml:"cors"`
}

type HealthConfig struct {
	Enabled bool   `yaml:"enabled"` // default: false
	Path    string `yaml:"path"`    // default /health when enabled
}

type ReadinessConfig struct {
	Enabled bool   `yaml:"enabled"` // default: false
	Path    string `yaml:"path"`    // default /ready when enabled
}

// MetricsConfig serves the metrics registry (the metrics module) in the
// Prometheus text format. The page is unauthenticated: keep it on an internal
// network or restrict the path at the gateway.
type MetricsConfig struct {
	Enabled bool   `yaml:"enabled"` // default: false; requires the metrics module
	Path    string `yaml:"path"`    // default /metrics when enabled
}

// GraphConfig enables the module graph inspection page. It is unauthenticated
// and shows the application's topology: restrict it at the gateway.
type GraphConfig struct {
	Enabled bool   `yaml:"enabled"` // default false: no route is registered
	Path    string `yaml:"path"`    // default /graph when enabled; also serves {path}/mermaid.min.js
}

type RoutesConfig struct {
	Health    HealthConfig    `yaml:"health"`
	Readiness ReadinessConfig `yaml:"readiness"`
	Graph     GraphConfig     `yaml:"graph"`
	Metrics   MetricsConfig   `yaml:"metrics"`
}

// SecurityConfig enables go-ams-router's middleware.Security: the security
// response headers of helmet with its defaults (CSP, HSTS, X-Frame-Options,
// Referrer-Policy, Cross-Origin-*, nosniff…). Only what differs from the
// defaults needs to be set. Values are validated at configuration.
type SecurityConfig struct {
	// Enabled is on unless set to false: an API answers with these headers
	// by default and opting out is explicit.
	Enabled *bool `yaml:"enabled"` // default: true
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
	IncludeSubdomains *bool         `yaml:"include_subdomains"` // default: false (true pins HTTPS on every subdomain)
	Preload           bool          `yaml:"preload"`            // default: false
}

// CORSConfig enables go-ams-router's middleware.CORS (the Express cors
// package semantics). It runs before route matching, so preflights are
// answered (204) for every path, private routes included.
type CORSConfig struct {
	Enabled bool `yaml:"enabled"` // default: false
	// Origins are exact origins ("https://app.example.com"). Allowing every
	// origin (Access-Control-Allow-Origin: *) must be explicit: ["*"] or
	// allow_any_origin: true. Enabled with neither origins nor patterns is a
	// configuration error, so turning CORS on can never silently open the API
	// to every website.
	Origins []string `yaml:"origins"`
	// AllowAnyOrigin is the explicit form of origins: ["*"].
	AllowAnyOrigin bool `yaml:"allow_any_origin"` // default: false
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
// (Opts.Tenant) and Server.TenantMiddleware read the tenant ID from.
type TenantConfig struct {
	Source string `yaml:"source"` // claim (Principal.TenantID from the JWT, default) | header
	Header string `yaml:"header"` // header read when source is header; default: "X-Tenant-ID"
}

// AuthConfig configures the bearer authentication of private typed routes
// (Opts.IsPrivate). It requires the jose module.
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
	Hosts   []string `yaml:"hosts"`   // default: ["http://{server.host|localhost}:{server.port}"], or https://…:{tls.port} when plain HTTP only redirects or is closed
	Accent  string   `yaml:"accent"`  // page accent color (hex #RGB[A] or #RRGGBB[AA]); default: "#007d9cbf"
}

// ServerConfig configures the http.Server. The timeouts are never zero after
// the defaults: a server without them keeps connections of slow or idle
// clients open indefinitely (Slowloris).
type ServerConfig struct {
	Host   string `yaml:"host"`   // "" = all interfaces
	Port   int    `yaml:"port"`   // plain HTTP (TCP); optional, default: 8080
	Header bool   `yaml:"header"` // print the nForge banner at startup; default: false
	// TLS enables HTTPS over TCP and HTTP/3 enables QUIC over UDP; both are
	// off by default (no silent protocol upgrade).
	TLS   ServerTLSConfig   `yaml:"tls"`
	HTTP3 ServerHTTP3Config `yaml:"http3"`
	// Handlers that stream or need longer than WriteTimeout extend their own
	// deadline with http.NewResponseController(w).SetWriteDeadline.
	ReadHeaderTimeout time.Duration `yaml:"read_header_timeout"` // time to read the request headers; default: 10s; <= read_timeout
	ReadTimeout       time.Duration `yaml:"read_timeout"`        // time to read the whole request, body included; default: 30s
	WriteTimeout      time.Duration `yaml:"write_timeout"`       // from the end of the request headers to the end of the response; default: 30s
	IdleTimeout       time.Duration `yaml:"idle_timeout"`        // keep-alive wait for the next request; default: 120s
	// MaxInflightRequests sheds load: when this many requests are running, the
	// next ones are answered 503 with Retry-After at once. Long-lived responses
	// give their slot back with BeginStream. Zero (default) means unlimited.
	MaxInflightRequests int `yaml:"max_inflight_requests"` // default: 0 (unlimited); at most 1000000
	// ShutdownDelay keeps the server up, still accepting, for this long after
	// the shutdown began and before the listeners close. The application is
	// already not ready (/ready answers 503) so load balancers and Kubernetes
	// can take the instance out of rotation without refused connections. It
	// counts against the shutdown deadline (app.WithShutdownTimeout, 15s by
	// default): set that to at least shutdown_delay plus the drain (10s).
	ShutdownDelay time.Duration `yaml:"shutdown_delay"` // default: 0s (close at once); at most 5m
	// HandlerTimeout is the deadline of every request context, so handlers,
	// queries and loaders that honor ctx stop when it passes (the write
	// timeout alone closes the connection but leaves the handler running).
	// It is opt-in: omitted or 0s means disabled, a positive duration enables
	// it for every route except streams, which release it with BeginStream.
	// For one route only, mount nhttp.Timeout(d) on it instead.
	HandlerTimeout time.Duration `yaml:"handler_timeout"` // default: disabled (0s); positive enables
	// MaxBodyBytes bounds every request body, including the routes that read
	// r.Body themselves; typed routes are also bound by codec.max_payload_bytes.
	MaxBodyBytes codec.ByteSize `yaml:"max_body_bytes"` // default: codec.max_payload_bytes; at most 1gb
}

// ServerTLSConfig configures HTTPS over TCP (http.server.tls). The certificate
// pair is also what http3.same_keycert_tls reuses, even when HTTPS is off.
type ServerTLSConfig struct {
	Enabled             bool   `yaml:"enabled"`               // default: false
	Port                int    `yaml:"port"`                  // TCP port; default: 8443
	CertFile            string `yaml:"cert_file"`             // PEM certificate chain; required when enabled
	KeyFile             string `yaml:"key_file"`              // PEM private key; required when enabled
	MinVersion          string `yaml:"min_version"`           // "1.3" (default) | "1.2"
	DisableInsecureHTTP bool   `yaml:"disable_insecure_http"` // close the plain HTTP listener; default: false
	RedirectHTTP        bool   `yaml:"redirect_http"`         // plain HTTP only redirects (308) to HTTPS; default: false
	// RedirectHost is the public host name or IP written into the redirect
	// Location. It is never taken from the request. Optional only when
	// http.server.host is a loopback address, which is then used.
	RedirectHost string `yaml:"redirect_host"`
}

// ServerHTTP3Config configures HTTP/3 over QUIC (http.server.http3).
type ServerHTTP3Config struct {
	Enabled bool `yaml:"enabled"` // default: false
	Port    int  `yaml:"port"`    // UDP port, independent of tls.port; default: 8443
	// SameKeyCertTLS reuses http.server.tls cert_file/key_file (default: true).
	SameKeyCertTLS *bool  `yaml:"same_keycert_tls"`
	CertFile       string `yaml:"cert_file"` // required only when same_keycert_tls is false
	KeyFile        string `yaml:"key_file"`  // required only when same_keycert_tls is false
	// Advertise sends Alt-Svc on HTTPS responses while QUIC is serving
	// (default: true; effective only with tls.enabled).
	Advertise *bool `yaml:"advertise"`
	// MaxIdleTimeout is the QUIC connection idle timeout; default: 30s.
	MaxIdleTimeout time.Duration `yaml:"max_idle_timeout"`
	// MaxStreamTimeout bounds one HTTP/3 request: context deadline plus stream
	// read/write deadlines; default: 30s. It is the HTTP/3 counterpart of
	// read_timeout/write_timeout, which only apply to TCP.
	MaxStreamTimeout time.Duration `yaml:"max_stream_timeout"`
	// Allow0RTT is reserved: 0-RTT requests are replayable and nForge has no
	// replay policy, so true is rejected. Default: false.
	Allow0RTT bool `yaml:"allow_0rtt"`
}

const redactedValue = "[REDACTED]"

// Redacted returns a copy without the private key paths.
func (c Config) Redacted() Config {
	if c.Server.TLS.KeyFile != "" {
		c.Server.TLS.KeyFile = redactedValue
	}
	if c.Server.HTTP3.KeyFile != "" {
		c.Server.HTTP3.KeyFile = redactedValue
	}
	return c
}

type plainConfig Config

// String prints the redacted configuration.
func (c Config) String() string { return fmt.Sprintf("%+v", plainConfig(c.Redacted())) }

// GoString prints the redacted configuration.
func (c Config) GoString() string { return fmt.Sprintf("%#v", plainConfig(c.Redacted())) }

// LogValue logs the redacted configuration.
func (c Config) LogValue() slog.Value { return slog.AnyValue(plainConfig(c.Redacted())) }

// MarshalJSON marshals the redacted configuration.
func (c Config) MarshalJSON() ([]byte, error) { return json.Marshal(plainConfig(c.Redacted())) }

// ApplyDefaults fills what the section leaves unset; app supplies the docs
// title and version.
func (c *Config) ApplyDefaults(app module.App) {
	ApplyServerDefaults(&c.Server)
	if c.Routes.Health.Enabled && c.Routes.Health.Path == "" {
		c.Routes.Health.Path = "/health"
	}
	if c.Routes.Readiness.Enabled && c.Routes.Readiness.Path == "" {
		c.Routes.Readiness.Path = "/ready"
	}
	if c.Routes.Metrics.Enabled && c.Routes.Metrics.Path == "" {
		c.Routes.Metrics.Path = "/metrics"
	}
	if c.Routes.Graph.Enabled && c.Routes.Graph.Path == "" {
		c.Routes.Graph.Path = DefaultGraphPath
	}
	docs := &c.Docs
	if docs.Path == "" {
		docs.Path = "/docs"
	}
	docs.Path = "/" + strings.Trim(docs.Path, "/")
	if docs.Title == "" {
		docs.Title = app.Name
	}
	if docs.Version == "" {
		docs.Version = app.Version
	}
	if docs.Auth == "" {
		docs.Auth = "bearer"
	}
	if docs.Accent == "" {
		docs.Accent = "#007d9cbf"
	}
	if len(docs.Hosts) == 0 {
		host := c.Server.Host
		if host == "" || host == "0.0.0.0" || host == "::" {
			host = "localhost"
		}
		docs.Hosts = []string{defaultDocsHost(c.Server, host)}
	}
	if c.Auth.Realm == "" {
		c.Auth.Realm = "nforge-api"
	}
	if c.Tenant.Source == "" {
		c.Tenant.Source = "claim"
	}
	if c.Tenant.Header == "" {
		c.Tenant.Header = "X-Tenant-ID"
	}
	feedSecurityDefaults(&c.Security)
	feedCORSDefaults(&c.CORS)
}

var hexColorRe = regexp.MustCompile(`^#([0-9a-fA-F]{3,4}|[0-9a-fA-F]{6}|[0-9a-fA-F]{8})$`)

var headerNameRe = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")

// Validate checks the section on its own; Module checks what involves other
// modules (the auth profile, the JWKS path) once the graph is resolved.
func (c *Config) Validate() error {
	if err := ValidateServer(c.Server); err != nil {
		return err
	}
	if auth := c.Docs.Auth; auth != "bearer" && auth != "none" {
		return fmt.Errorf("http.docs.auth must be bearer or none, got %q", auth)
	}
	if accent := c.Docs.Accent; !hexColorRe.MatchString(accent) {
		return fmt.Errorf("http.docs.accent must be a hex color (#RGB, #RGBA, #RRGGBB or #RRGGBBAA), got %q", accent)
	}
	if err := ValidateGraphRoute(c, ""); err != nil {
		return err
	}
	if err := validateRealm(c.Auth.Realm); err != nil {
		return err
	}
	switch c.Tenant.Source {
	case "claim", "header":
	default:
		return fmt.Errorf("http.tenant.source must be claim or header, got %q", c.Tenant.Source)
	}
	if !headerNameRe.MatchString(c.Tenant.Header) {
		return fmt.Errorf("http.tenant.header %q is not a valid header name", c.Tenant.Header)
	}
	if err := validateSecurity(c.Security); err != nil {
		return err
	}
	return validateCORS(c.CORS)
}

// validateRealm checks http.auth.realm. It goes into a quoted-string of
// WWW-Authenticate, so quotes, backslashes and control characters are rejected.
func validateRealm(realm string) error {
	if len(realm) > 128 {
		return fmt.Errorf("http.auth.realm must have at most 128 characters")
	}
	for i := 0; i < len(realm); i++ {
		if c := realm[i]; c < 0x20 || c > 0x7e || c == '"' || c == '\\' {
			return fmt.Errorf("http.auth.realm must be printable ASCII without quotes or backslashes")
		}
	}
	return nil
}
