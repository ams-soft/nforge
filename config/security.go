package config

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

// feedSecurityDefaults writes helmet's defaults into http.security so the
// loaded config shows what is sent.
func feedSecurityDefaults(sec *SecurityConfig) {
	if sec.FrameOptions == "" {
		sec.FrameOptions = "SAMEORIGIN"
	}
	if len(sec.ReferrerPolicy) == 0 {
		sec.ReferrerPolicy = []string{"no-referrer"}
	}
	if sec.CrossOriginOpenerPolicy == "" {
		sec.CrossOriginOpenerPolicy = "same-origin"
	}
	if sec.CrossOriginResourcePolicy == "" {
		sec.CrossOriginResourcePolicy = "same-origin"
	}
	if sec.HSTS.MaxAge == 0 {
		sec.HSTS.MaxAge = 365 * 24 * time.Hour
	}
	if sec.HSTS.IncludeSubdomains == nil {
		include := true
		sec.HSTS.IncludeSubdomains = &include
	}
}

func feedCORSDefaults(c *CORSConfig) {
	if len(c.Methods) == 0 {
		c.Methods = []string{"GET", "HEAD", "PUT", "PATCH", "POST", "DELETE"}
	}
	if c.OptionsSuccessStatus == 0 {
		c.OptionsSuccessStatus = 204
	}
}

// securityHeaders are the headers http.security.disable accepts, lowercase.
// Cross-Origin-Embedder-Policy is absent: it is off unless configured.
var securityHeaders = []string{
	"content-security-policy", "cross-origin-opener-policy", "cross-origin-resource-policy",
	"origin-agent-cluster", "referrer-policy", "strict-transport-security",
	"x-content-type-options", "x-dns-prefetch-control", "x-download-options",
	"x-frame-options", "x-permitted-cross-domain-policies", "x-xss-protection",
}

// cspKeywords must be quoted in a policy ("'self'"); unquoted they are host
// names, a silent misconfiguration the router also rejects.
var cspKeywords = []string{
	"self", "none", "strict-dynamic", "report-sample", "inline-speculation-rules",
	"unsafe-inline", "unsafe-eval", "unsafe-hashes", "wasm-unsafe-eval",
}

var cspDirectiveRe = regexp.MustCompile(`^[a-z][a-z0-9-]*[a-z0-9]$`)

// validateSecurity checks http.security when enabled, with the same rules
// as the router's middleware.Security (which would otherwise panic when the
// server is built).
func validateSecurity(sec SecurityConfig) error {
	if !sec.Enabled {
		return nil
	}
	for _, h := range sec.Disable {
		if !slices.Contains(securityHeaders, strings.ToLower(h)) {
			return fmt.Errorf("http.security.disable: unknown header %q (one of %s)", h, strings.Join(securityHeaders, ", "))
		}
	}
	enums := []struct {
		field, value string
		allowed      []string
	}{
		{"frame_options", strings.ToUpper(sec.FrameOptions), []string{"SAMEORIGIN", "DENY"}},
		{"cross_origin_opener_policy", sec.CrossOriginOpenerPolicy, []string{"same-origin", "same-origin-allow-popups", "unsafe-none"}},
		{"cross_origin_resource_policy", sec.CrossOriginResourcePolicy, []string{"same-origin", "same-site", "cross-origin"}},
		{"cross_origin_embedder_policy", sec.CrossOriginEmbedderPolicy, []string{"", "require-corp", "credentialless", "unsafe-none"}},
	}
	for _, e := range enums {
		if !slices.Contains(e.allowed, e.value) {
			return fmt.Errorf("http.security.%s: invalid value %q", e.field, e.value)
		}
	}
	referrers := []string{
		"no-referrer", "no-referrer-when-downgrade", "same-origin", "origin", "strict-origin",
		"origin-when-cross-origin", "strict-origin-when-cross-origin", "unsafe-url",
	}
	for i, p := range sec.ReferrerPolicy {
		if !slices.Contains(referrers, p) || slices.Contains(sec.ReferrerPolicy[:i], p) {
			return fmt.Errorf("http.security.referrer_policy: invalid or repeated value %q", p)
		}
	}
	if sec.HSTS.MaxAge < 0 || sec.HSTS.MaxAge%time.Second != 0 {
		return fmt.Errorf("http.security.hsts.max_age must be a non-negative whole number of seconds")
	}
	return validateCSP(sec.CSP)
}

func validateCSP(csp CSPConfig) error {
	hasDefaultSrc := !csp.DisableDefaults
	for name, values := range csp.Directives {
		if !cspDirectiveRe.MatchString(name) {
			return fmt.Errorf("http.security.csp.directives: invalid directive name %q (use kebab-case, e.g. script-src)", name)
		}
		if name == "default-src" {
			hasDefaultSrc = values != nil
		}
		for _, v := range values {
			if v == "" || strings.ContainsAny(v, ";,\r\n\t ") {
				return fmt.Errorf("http.security.csp.directives.%s: invalid value %q", name, v)
			}
			lower := strings.ToLower(v)
			if slices.Contains(cspKeywords, lower) || strings.HasPrefix(lower, "nonce-") ||
				strings.HasPrefix(lower, "sha256-") || strings.HasPrefix(lower, "sha384-") || strings.HasPrefix(lower, "sha512-") {
				return fmt.Errorf("http.security.csp.directives.%s: %q must be quoted: \"'%s'\"", name, v, v)
			}
		}
	}
	if !hasDefaultSrc {
		return fmt.Errorf("http.security.csp: default-src is required")
	}
	return nil
}

// validateCORS checks http.cors when enabled. Origins are compared byte for
// byte with the Origin header, which browsers send as lowercase
// scheme://host[:port], so anything else would never match.
func validateCORS(c CORSConfig) error {
	if !c.Enabled {
		return nil
	}
	anyOrigin := len(c.Origins) == 0 && len(c.OriginPatterns) == 0
	for _, o := range c.Origins {
		if o == "*" {
			if len(c.Origins) > 1 || len(c.OriginPatterns) > 0 {
				return fmt.Errorf("http.cors.origins: \"*\" cannot be combined with other origins or patterns")
			}
			anyOrigin = true
			continue
		}
		u, err := url.Parse(o)
		if err != nil || u.Scheme == "" || u.Host == "" || u.Scheme+"://"+u.Host != o || o != strings.ToLower(o) {
			return fmt.Errorf("http.cors.origins: %q must be a lowercase origin like https://app.example.com (no path or trailing slash)", o)
		}
	}
	for _, p := range c.OriginPatterns {
		if !strings.HasPrefix(p, "^") || !strings.HasSuffix(p, "$") {
			return fmt.Errorf("http.cors.origin_patterns: %q must be anchored with ^ and $", p)
		}
		if _, err := regexp.Compile(p); err != nil {
			return fmt.Errorf("http.cors.origin_patterns: %q: %w", p, err)
		}
	}
	if c.Credentials && anyOrigin {
		return fmt.Errorf("http.cors.credentials requires explicit origins or origin_patterns (browsers reject credentials with \"*\")")
	}
	for _, m := range c.Methods {
		if !headerNameRe.MatchString(m) {
			return fmt.Errorf("http.cors.methods: invalid method %q", m)
		}
	}
	for field, list := range map[string][]string{"allowed_headers": c.AllowedHeaders, "exposed_headers": c.ExposedHeaders} {
		for _, h := range list {
			if h == "*" && c.Credentials {
				return fmt.Errorf("http.cors.%s: \"*\" is literal with credentials; list the headers", field)
			}
			if h != "*" && !headerNameRe.MatchString(h) {
				return fmt.Errorf("http.cors.%s: invalid header name %q", field, h)
			}
		}
	}
	if c.MaxAge < 0 || c.MaxAge%time.Second != 0 {
		return fmt.Errorf("http.cors.max_age must be a non-negative whole number of seconds")
	}
	if c.OptionsSuccessStatus < 200 || c.OptionsSuccessStatus > 299 {
		return fmt.Errorf("http.cors.options_success_status must be between 200 and 299")
	}
	return nil
}
