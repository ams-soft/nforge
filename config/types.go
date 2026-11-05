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

	// PrdExceptions accepts, one by one, the insecure settings that env prd
	// otherwise refuses to load (they are only warnings in other environments).
	PrdExceptions PrdExceptionsConfig `yaml:"prd_exceptions"`

	// Warnings are problems the loader accepted (an unset ${VAR} that expanded
	// to an empty string, ...). deps.NewDeps logs them once the logger exists.
	Warnings []string `yaml:"-"`
}

type AppConfig struct {
	Name        string `yaml:"name"`
	Version     string `yaml:"version"`
	OnlineSince string `yaml:"-"` // set at load time
	// InternalUse declares that the service is not reachable from the internet
	// (default false). It only silences the prd warning about the docs, health
	// and readiness endpoints; every other check is unaffected.
	InternalUse bool `yaml:"internal_use"`
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
	Hosts   []string `yaml:"hosts"`   // default: ["http://{server.host|localhost}:{server.port}"], or https://…:{tls.port} when plain HTTP only redirects or is closed
	Accent  string   `yaml:"accent"`  // page accent color (hex #RGB[A] or #RRGGBB[AA]); default: "#007d9cbf"
}

// ServerConfig configures the http.Server. The timeouts are never zero after
// load: a server without them keeps connections of slow or idle clients
// open indefinitely (Slowloris).
type ServerConfig struct {
	Host   string `yaml:"host"`
	Port   int    `yaml:"port"` // plain HTTP (TCP); optional, default: 8080
	Header bool   `yaml:"header"`
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
	// HandlerTimeout is the deadline of every request context, so handlers,
	// queries and loaders that honor ctx stop when it passes (the write
	// timeout alone closes the connection but leaves the handler running).
	HandlerTimeout time.Duration `yaml:"handler_timeout"` // default: 30s
	// MaxBodyBytes bounds every request body, including the routes that read
	// r.Body themselves; typed routes are also bound by codec.max_payload_bytes.
	MaxBodyBytes ByteSize `yaml:"max_body_bytes"` // default: codec.max_payload_bytes; at most 1gb
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
	// replay policy, so true is rejected at load. Default: false.
	Allow0RTT bool `yaml:"allow_0rtt"`
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

	// SampleRatio is the fraction of requests whose spans are recorded and
	// exported, decided from the trace id; a request that arrives with a
	// sampled parent is still recorded when trust_incoming is on.
	SampleRatio *float64 `yaml:"sample_ratio"` // 0..1; default: 1 (every request)
	// TrustIncoming makes the HTTP middleware continue a traceparent, adopt a
	// valid X-Request-Id as the trace id and propagate baggage received from
	// the caller. Behind a gateway or between services that is what links the
	// traces, so turn it on there. It is off by default: every request then
	// starts its own trace, so a client of an exposed API can neither force
	// sampling, nor reuse someone else's trace id, nor inject baggage.
	TrustIncoming *bool `yaml:"trust_incoming"` // default: false
}

// Ratio returns the effective sample ratio.
func (t TracingOtel) Ratio() float64 {
	if t.SampleRatio == nil {
		return 1
	}
	return *t.SampleRatio
}

// TrustsIncoming reports whether inbound trace context is trusted (opt-in).
func (t TracingOtel) TrustsIncoming() bool { return t.TrustIncoming != nil && *t.TrustIncoming }

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

// PrdExceptionsConfig lists the insecure settings an operator accepts in
// production on purpose. Every flag defaults to false: with env prd the
// matching setting is then a load error instead of a startup warning.
type PrdExceptionsConfig struct {
	TLSKeyPermissions bool `yaml:"tls_key_permissions"` // TLS private key readable by group or others
	RedisPlaintext    bool `yaml:"redis_plaintext"`     // Redis without TLS (credentials, or behind HTTPS/HTTP/3)
	DatabasePlaintext bool `yaml:"database_plaintext"`  // PostgreSQL sslmode that can fall back to clear text
}

// allows reports whether the named exception is set.
func (e PrdExceptionsConfig) allows(name string) bool {
	switch name {
	case "tls_key_permissions":
		return e.TLSKeyPermissions
	case "redis_plaintext":
		return e.RedisPlaintext
	case "database_plaintext":
		return e.DatabasePlaintext
	}
	return false
}
