package config

import (
	"strings"
	"testing"
	"time"
)

func TestSecurityAndCORSDefaults(t *testing.T) {
	cfg, err := loadTenantConfig(t, "http: {server: {port: 8080}}", "")
	if err != nil {
		t.Fatal(err)
	}
	sec, cors := cfg.HTTP.Security, cfg.HTTP.CORS
	if sec.Enabled || cors.Enabled {
		t.Fatal("security and cors must be disabled by default")
	}
	if sec.FrameOptions != "SAMEORIGIN" || strings.Join(sec.ReferrerPolicy, ",") != "no-referrer" ||
		sec.CrossOriginOpenerPolicy != "same-origin" || sec.CrossOriginResourcePolicy != "same-origin" ||
		sec.CrossOriginEmbedderPolicy != "" || sec.HSTS.MaxAge != 365*24*time.Hour ||
		sec.HSTS.IncludeSubdomains == nil || !*sec.HSTS.IncludeSubdomains {
		t.Fatalf("http.security = %+v", sec)
	}
	if strings.Join(cors.Methods, ",") != "GET,HEAD,PUT,PATCH,POST,DELETE" || cors.OptionsSuccessStatus != 204 {
		t.Fatalf("http.cors = %+v", cors)
	}
}

func TestSecurityAndCORSYAMLAndEnvFile(t *testing.T) {
	yamlText := `
http:
  server: {port: 8080}
  security:
    enabled: true
    disable: [X-Frame-Options]
    csp:
      directives:
        script-src: ["'self'", "https://cdn.example.com"]
        upgrade-insecure-requests: ~
        block-all-mixed-content: []
  cors:
    enabled: true
    origins: [https://app.example.com]
    origin_patterns: ['^https://[a-z]+\.example\.com$']
    credentials: true
`
	cfg, err := loadTenantConfig(t, yamlText, "HTTP_SECURITY_HSTS_MAX_AGE=1h\nHTTP_SECURITY_HSTS_INCLUDE_SUBDOMAINS=false\nHTTP_CORS_EXPOSED_HEADERS=X-Request-Id,ETag\nHTTP_CORS_MAX_AGE=10m\n")
	if err != nil {
		t.Fatal(err)
	}
	sec, cors := cfg.HTTP.Security, cfg.HTTP.CORS
	d := sec.CSP.Directives
	if v, ok := d["upgrade-insecure-requests"]; !ok || v != nil {
		t.Errorf("null directive must be kept as nil (removal): %#v", v)
	}
	if v := d["block-all-mixed-content"]; v == nil || len(v) != 0 {
		t.Errorf("[] directive must be non-nil and empty: %#v", v)
	}
	if len(d["script-src"]) != 2 || sec.HSTS.MaxAge != time.Hour || *sec.HSTS.IncludeSubdomains || sec.Disable[0] != "X-Frame-Options" {
		t.Errorf("http.security = %+v", sec)
	}
	if strings.Join(cors.ExposedHeaders, ",") != "X-Request-Id,ETag" || cors.MaxAge != 10*time.Minute || !cors.Credentials {
		t.Errorf("http.cors = %+v", cors)
	}
}

func TestSecurityAndCORSValidation(t *testing.T) {
	cases := map[string]string{
		// security
		"security: {enabled: true, disable: [x-powered-by]}":                                  "unknown header",
		"security: {enabled: true, frame_options: ALLOW-FROM}":                                "frame_options",
		"security: {enabled: true, referrer_policy: [origin, origin]}":                        "referrer_policy",
		"security: {enabled: true, cross_origin_embedder_policy: corp}":                       "cross_origin_embedder_policy",
		"security: {enabled: true, hsts: {max_age: 1500ms}}":                                  "hsts.max_age",
		"security: {enabled: true, csp: {directives: {script-src: [self]}}}":                  "must be quoted",
		"security: {enabled: true, csp: {directives: {script-src: [\"'self'; x\"]}}}":         "invalid value",
		"security: {enabled: true, csp: {directives: {scriptSrc: [\"'self'\"]}}}":             "kebab-case",
		"security: {enabled: true, csp: {directives: {default-src: ~}}}":                      "default-src is required",
		"security: {enabled: true, csp: {disable_defaults: true, directives: {img-src: []}}}": "default-src is required",
		// cors
		"cors: {enabled: true, origins: ['*', 'https://a.com']}":                                       "cannot be combined",
		"cors: {enabled: true, origins: ['https://a.com/']}":                                           "lowercase origin",
		"cors: {enabled: true, origins: ['https://A.com']}":                                            "lowercase origin",
		"cors: {enabled: true, origins: ['a.com']}":                                                    "lowercase origin",
		"cors: {enabled: true, origin_patterns: ['https://.*\\.a\\.com']}":                             "anchored",
		"cors: {enabled: true, origin_patterns: ['^(https$']}":                                         "origin_patterns",
		"cors: {enabled: true, credentials: true}":                                                     "explicit origins",
		"cors: {enabled: true, origins: ['*'], credentials: true}":                                     "explicit origins",
		"cors: {enabled: true, methods: ['GET POST']}":                                                 "invalid method",
		"cors: {enabled: true, exposed_headers: ['X Bad']}":                                            "invalid header name",
		"cors: {enabled: true, origins: ['https://a.com'], credentials: true, allowed_headers: ['*']}": "literal with credentials",
		"cors: {enabled: true, max_age: -1s}":                                                          "max_age",
		"cors: {enabled: true, options_success_status: 302}":                                           "options_success_status",
	}
	for section, want := range cases {
		_, err := loadTenantConfig(t, "http:\n  server: {port: 8080}\n  "+section+"\n", "")
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", section, err, want)
		}
	}

	// Disabled sections are not validated.
	if _, err := loadTenantConfig(t, "http:\n  server: {port: 8080}\n  security: {frame_options: x}\n  cors: {credentials: true}\n", ""); err != nil {
		t.Errorf("disabled sections: %v", err)
	}
	// Maps are YAML only.
	if _, err := loadTenantConfig(t, "http: {server: {port: 8080}}", "HTTP_SECURITY_CSP_DIRECTIVES=x\n"); err == nil || !strings.Contains(err.Error(), "maps cannot be set") {
		t.Errorf("env map: %v", err)
	}
}
