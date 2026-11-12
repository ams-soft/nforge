package nhttp

import (
	"crypto/tls"

	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ams-soft/nforge/codec"
)

const (
	defaultReadHeaderTimeout = 10 * time.Second
	defaultReadTimeout       = 30 * time.Second
	defaultWriteTimeout      = 30 * time.Second
	defaultIdleTimeout       = 120 * time.Second

	// maxBodyCeiling is the largest accepted http.server.max_body_bytes.
	maxBodyCeiling = 1 * codec.GB

	// DefaultHTTPPort is the plain HTTP (TCP) port; DefaultTLSPort the HTTPS
	// (TCP) port and DefaultHTTP3Port the HTTP/3 (UDP) port. The last two
	// share a number on purpose: TCP and UDP do not collide, and each is
	// defaulted independently of the other.
	DefaultHTTPPort  = 8080
	DefaultTLSPort   = 8443
	DefaultHTTP3Port = 8443

	defaultHTTP3IdleTimeout   = 30 * time.Second
	defaultHTTP3StreamTimeout = 30 * time.Second

	// TLSVersion12 and TLSVersion13 are the only accepted tls.min_version values.
	TLSVersion12 = "1.2"
	TLSVersion13 = "1.3"
)

// ApplyServerDefaults fills the http.server ports, transport options and
// timeouts left unset (zero or nil). The module calls it at configuration;
// New and Start call it again so a Config built by hand (tests, embedding)
// still gets a server with timeouts. There is deliberately
// no way to disable the transport timeouts: net/http treats zero as "no
// timeout", the unsafe default this exists to avoid. handler_timeout is the
// exception: it is a request-context deadline, off unless positive.
func ApplyServerDefaults(s *ServerConfig) {
	setPort(&s.Port, DefaultHTTPPort)
	setPort(&s.TLS.Port, DefaultTLSPort)
	setPort(&s.HTTP3.Port, DefaultHTTP3Port)
	if s.TLS.MinVersion == "" {
		s.TLS.MinVersion = TLSVersion13
	}
	if s.HTTP3.SameKeyCertTLS == nil {
		s.HTTP3.SameKeyCertTLS = new(true)
	}
	if s.HTTP3.Advertise == nil {
		s.HTTP3.Advertise = new(true)
	}
	setDuration(&s.HTTP3.MaxIdleTimeout, defaultHTTP3IdleTimeout)
	setDuration(&s.HTTP3.MaxStreamTimeout, defaultHTTP3StreamTimeout)
	setDuration(&s.ReadHeaderTimeout, defaultReadHeaderTimeout)
	setDuration(&s.ReadTimeout, defaultReadTimeout)
	setDuration(&s.WriteTimeout, defaultWriteTimeout)
	setDuration(&s.IdleTimeout, defaultIdleTimeout)
	// HandlerTimeout stays 0 (disabled): a request deadline is an opt-in.
}

func setDuration(d *time.Duration, def time.Duration) {
	if *d == 0 {
		*d = def
	}
}

func setPort(p *int, def int) {
	if *p == 0 {
		*p = def
	}
}

// SameKeyCert reports whether HTTP/3 reuses the http.server.tls certificate
// pair (the default when unset).
func (h ServerHTTP3Config) SameKeyCert() bool { return h.SameKeyCertTLS == nil || *h.SameKeyCertTLS }

// AdvertiseAltSvc reports whether HTTPS responses announce HTTP/3 (the default
// when unset).
func (h ServerHTTP3Config) AdvertiseAltSvc() bool { return h.Advertise == nil || *h.Advertise }

// HTTPListener reports whether the plain HTTP (TCP) listener runs, as the
// application or, with tls.redirect_http, as a redirect-only server.
func (s ServerConfig) HTTPListener() bool { return !s.TLS.DisableInsecureHTTP }

// HTTP3Credentials returns the certificate and key files HTTP/3 loads.
func (s ServerConfig) HTTP3Credentials() (certFile, keyFile string) {
	if s.HTTP3.SameKeyCert() {
		return s.TLS.CertFile, s.TLS.KeyFile
	}
	return s.HTTP3.CertFile, s.HTTP3.KeyFile
}

// RedirectAuthority returns the host[:port] the redirect-only HTTP listener
// writes into Location. It comes from configuration only (redirect_host, or a
// loopback http.server.host), never from the request, so a forged Host header
// cannot turn the redirect into an open redirect. The port is omitted for 443.
func (s ServerConfig) RedirectAuthority() (string, error) {
	host := s.TLS.RedirectHost
	if host == "" {
		if !isLoopbackHost(s.Host) {
			return "", fmt.Errorf("http.server.tls.redirect_host is required when redirect_http is true and http.server.host (%q) is not a loopback address", s.Host)
		}
		host = s.Host
	}
	if err := validateRedirectHost(host); err != nil {
		return "", err
	}
	port := s.TLS.Port
	if port == 0 {
		port = DefaultTLSPort
	}
	if port == 443 {
		if strings.Contains(host, ":") {
			return "[" + host + "]", nil
		}
		return host, nil
	}
	return net.JoinHostPort(host, strconv.Itoa(port)), nil
}

func isLoopbackHost(h string) bool {
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

var hostnameRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$`)

// validateRedirectHost accepts a bare host name or IP literal: no scheme,
// port, path, userinfo, zone, wildcard address or control characters.
func validateRedirectHost(h string) error {
	if ip := net.ParseIP(h); ip != nil {
		if ip.IsUnspecified() {
			return fmt.Errorf("http.server.tls.redirect_host must be a public host, not a wildcard address")
		}
		return nil
	}
	if len(h) > 253 || !hostnameRe.MatchString(h) {
		return fmt.Errorf("http.server.tls.redirect_host must be a host name or IP address without scheme, port or path")
	}
	return nil
}

// defaultDocsHost is the generated OpenAPI server URL: https when the plain
// HTTP listener only redirects or is closed and an HTTPS (or HTTP/3, which is
// https too) endpoint exists; http otherwise. host is already a usable name.
func defaultDocsHost(s ServerConfig, host string) string {
	switch {
	case s.TLS.Enabled && (s.TLS.RedirectHTTP || s.TLS.DisableInsecureHTTP):
		return "https://" + net.JoinHostPort(host, strconv.Itoa(s.TLS.Port))
	case s.HTTP3.Enabled && s.TLS.DisableInsecureHTTP:
		return "https://" + net.JoinHostPort(host, strconv.Itoa(s.HTTP3.Port))
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(s.Port))
}

// ValidateServer checks http.server after defaults (it applies them to a copy,
// so it also accepts a Config built by hand): port ranges, timeouts, the TLS
// and HTTP/3 settings, listener collisions and, for every enabled transport,
// that the certificate pair loads and matches. nhttp calls it again at Start.
func ValidateServer(s ServerConfig) error {
	ApplyServerDefaults(&s)
	for _, p := range []struct {
		field string
		value int
	}{{"port", s.Port}, {"tls.port", s.TLS.Port}, {"http3.port", s.HTTP3.Port}} {
		if p.value < 1 || p.value > 65535 {
			return fmt.Errorf("http.server.%s must be between 1 and 65535, got %d", p.field, p.value)
		}
	}
	if err := validateTimeouts(s); err != nil {
		return err
	}
	return validateTransports(s)
}

func validateTransports(s ServerConfig) error {
	t, h := s.TLS, s.HTTP3
	switch t.MinVersion {
	case TLSVersion12, TLSVersion13:
	default:
		return fmt.Errorf("http.server.tls.min_version must be %q or %q, got %q", TLSVersion13, TLSVersion12, t.MinVersion)
	}
	if t.DisableInsecureHTTP && t.RedirectHTTP {
		return fmt.Errorf("http.server.tls.disable_insecure_http and redirect_http cannot both be true")
	}
	if t.RedirectHTTP && !t.Enabled {
		return fmt.Errorf("http.server.tls.redirect_http requires http.server.tls.enabled")
	}
	if t.DisableInsecureHTTP && !t.Enabled && !h.Enabled {
		return fmt.Errorf("http.server.tls.disable_insecure_http would leave no transport: enable tls or http3")
	}
	if h.Allow0RTT {
		return fmt.Errorf("http.server.http3.allow_0rtt is reserved: 0-RTT requests are replayable and nForge has no replay policy")
	}
	if h.MaxIdleTimeout < 0 || h.MaxStreamTimeout < 0 {
		return fmt.Errorf("http.server.http3 timeouts must be positive")
	}
	if t.RedirectHTTP {
		if _, err := s.RedirectAuthority(); err != nil {
			return err
		}
	} else if t.RedirectHost != "" {
		if err := validateRedirectHost(t.RedirectHost); err != nil {
			return err
		}
	}
	if s.HTTPListener() && t.Enabled && s.Port == t.Port {
		return fmt.Errorf("http.server.port and http.server.tls.port are both %d on TCP: pick different ports or set disable_insecure_http", s.Port)
	}
	if t.Enabled {
		if err := checkKeyPair("http.server.tls", t.CertFile, t.KeyFile); err != nil {
			return err
		}
	}
	if h.Enabled {
		if h.SameKeyCert() {
			if h.CertFile != "" || h.KeyFile != "" {
				return fmt.Errorf("http.server.http3.cert_file/key_file conflict with same_keycert_tls: true; set same_keycert_tls: false or remove them")
			}
			if !t.Enabled { // already checked above when TLS is enabled
				if err := checkKeyPair("http.server.tls (reused by http3)", t.CertFile, t.KeyFile); err != nil {
					return err
				}
			}
		} else if err := checkKeyPair("http.server.http3", h.CertFile, h.KeyFile); err != nil {
			return err
		}
	}
	return nil
}

// checkKeyPair requires both files and a loadable, matching pair. The error
// names the section and the failure, never key material.
func checkKeyPair(section, certFile, keyFile string) error {
	if certFile == "" || keyFile == "" {
		return fmt.Errorf("%s: cert_file and key_file are both required", section)
	}
	if _, err := tls.LoadX509KeyPair(certFile, keyFile); err != nil {
		return fmt.Errorf("%s: cannot load the certificate and key pair: %w", section, err)
	}
	return nil
}

// validateTimeouts rejects negative timeouts (net/http would read them as
// already expired) and a header timeout longer than the whole-request one,
// which could never apply.
func validateTimeouts(s ServerConfig) error {
	timeouts := []struct {
		field string
		value time.Duration
	}{
		{"read_header_timeout", s.ReadHeaderTimeout},
		{"read_timeout", s.ReadTimeout},
		{"write_timeout", s.WriteTimeout},
		{"idle_timeout", s.IdleTimeout},
		{"handler_timeout", s.HandlerTimeout},
	}
	for _, t := range timeouts {
		if t.value < 0 {
			return fmt.Errorf("http.server.%s must be positive, got %s", t.field, t.value)
		}
	}
	if s.MaxBodyBytes < 0 || s.MaxBodyBytes > maxBodyCeiling {
		return fmt.Errorf("http.server.max_body_bytes must be between 0 and 1gb, got %d", int64(s.MaxBodyBytes))
	}
	if s.ReadHeaderTimeout > s.ReadTimeout {
		return fmt.Errorf("http.server.read_header_timeout (%s) must not exceed http.server.read_timeout (%s)", s.ReadHeaderTimeout, s.ReadTimeout)
	}
	return nil
}
