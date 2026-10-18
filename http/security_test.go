package nhttp

import (
	"log/slog"
	stdhttp "net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ams-soft/nforge/config"
	"github.com/ams-soft/nforge/deps"
)

func newSecurityServer(t *testing.T, mutate func(*config.Config)) *ServerHttp {
	t.Helper()
	var cfg config.Config
	cfg.App = config.AppConfig{Name: "t", Version: "1.0.0"}
	cfg.HTTP.Routes.Health = config.HealthConfig{Enabled: true, Path: "/health"}
	cfg.HTTP.Docs = config.DocsConfig{
		Enabled: true, Path: "/docs", Title: "T", Version: "1", Auth: "bearer",
		Hosts: []string{"https://api.example.com/v1"}, Accent: "#007d9c",
	}
	cfg.HTTP.Auth.DefaultProfile = "access"
	mutate(&cfg)
	s := NewHTTPServer(deps.Deps{Config: cfg, Logger: *slog.New(slog.DiscardHandler), JOSE: sharedJOSE()})
	s.Typed().TPost("/private", nil, nil, func(stdhttp.ResponseWriter, *stdhttp.Request) {
		t.Error("private handler must not run on a preflight")
	}, Opts{IsPrivate: true})
	return s
}

func serveReq(s *ServerHttp, req *stdhttp.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s.Srv.Handler.ServeHTTP(rec, req)
	return rec
}

func TestSecurityHeaders(t *testing.T) {
	include := false
	s := newSecurityServer(t, func(c *config.Config) {
		c.HTTP.Security = config.SecurityConfig{
			Enabled: true,
			Disable: []string{"X-Frame-Options"},
			CSP: config.CSPConfig{Directives: map[string][]string{
				"script-src":                {"'self'", "https://cdn.example.com"},
				"upgrade-insecure-requests": nil,
			}},
			HSTS:           config.HSTSConfig{MaxAge: time.Hour, IncludeSubdomains: &include, Preload: true},
			ReferrerPolicy: []string{"strict-origin"},
		}
	})
	h := serveReq(s, httptest.NewRequest("GET", "/health", nil)).Header()

	csp := h.Get("Content-Security-Policy")
	if !strings.Contains(csp, "default-src 'self'") || !strings.Contains(csp, "script-src 'self' https://cdn.example.com") ||
		strings.Contains(csp, "upgrade-insecure-requests") {
		t.Errorf("CSP %q", csp)
	}
	want := map[string]string{
		"Strict-Transport-Security":  "max-age=3600; preload",
		"Referrer-Policy":            "strict-origin",
		"X-Content-Type-Options":     "nosniff",
		"Cross-Origin-Opener-Policy": "same-origin",
		"X-Frame-Options":            "",
	}
	for k, v := range want {
		if got := h.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
}

var nonceRe = regexp.MustCompile(`<script nonce="([^"]+)"`)

func TestDocsPageCSP(t *testing.T) {
	cases := []struct {
		name         string
		sec          config.SecurityConfig
		header, none string
	}{
		{"enforced", config.SecurityConfig{Enabled: true}, "Content-Security-Policy", "Content-Security-Policy-Report-Only"},
		{"report only", config.SecurityConfig{Enabled: true, CSP: config.CSPConfig{ReportOnly: true}}, "Content-Security-Policy-Report-Only", "Content-Security-Policy"},
		{"csp disabled", config.SecurityConfig{Enabled: true, Disable: []string{"content-security-policy"}}, "", "Content-Security-Policy"},
		{"security off", config.SecurityConfig{}, "", "Content-Security-Policy"},
	}
	for _, c := range cases {
		s := newSecurityServer(t, func(cfg *config.Config) { cfg.HTTP.Security = c.sec })
		rec := serveReq(s, httptest.NewRequest("GET", "/docs", nil))
		m := nonceRe.FindAllStringSubmatch(rec.Body.String(), -1)
		if len(m) != 3 || m[0][1] != m[1][1] || m[1][1] != m[2][1] {
			t.Fatalf("%s: every script needs the same nonce: %v", c.name, m)
		}
		if rec.Header().Get(c.none) != "" {
			t.Errorf("%s: unexpected %s", c.name, c.none)
		}
		if c.header == "" {
			continue
		}
		csp := rec.Header().Get(c.header)
		if !strings.Contains(csp, "script-src 'nonce-"+m[0][1]+"' https://cdn.jsdelivr.net") ||
			!strings.Contains(csp, "connect-src 'self' https://api.example.com") {
			t.Errorf("%s: docs CSP %q", c.name, csp)
		}
	}

	// A fresh nonce per render.
	s := newSecurityServer(t, func(cfg *config.Config) { cfg.HTTP.Security.Enabled = true })
	a := nonceRe.FindStringSubmatch(serveReq(s, httptest.NewRequest("GET", "/docs", nil)).Body.String())
	b := nonceRe.FindStringSubmatch(serveReq(s, httptest.NewRequest("GET", "/docs", nil)).Body.String())
	if a[1] == b[1] {
		t.Error("nonce reused across requests")
	}
	// The attribute must equal the header for any random value (html/template
	// escapes some base64 characters in attributes).
	for range 64 {
		rec := serveReq(s, httptest.NewRequest("GET", "/docs", nil))
		n := nonceRe.FindStringSubmatch(rec.Body.String())[1]
		if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "'nonce-"+n+"'") {
			t.Fatalf("nonce %q differs from the header", n)
		}
	}
}

func TestCORS(t *testing.T) {
	s := newSecurityServer(t, func(c *config.Config) {
		c.HTTP.CORS = config.CORSConfig{
			Enabled:              true,
			Origins:              []string{"https://app.example.com"},
			OriginPatterns:       []string{`^https://[a-z]+\.preview\.example\.com$`},
			Methods:              []string{"GET", "POST"},
			ExposedHeaders:       []string{"X-Request-Id"},
			Credentials:          true,
			MaxAge:               10 * time.Minute,
			OptionsSuccessStatus: 204,
		}
	})

	preflight := func(origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("OPTIONS", "/private", nil)
		req.Header.Set("Origin", origin)
		req.Header.Set("Access-Control-Request-Method", "POST")
		req.Header.Set("Access-Control-Request-Headers", "authorization,content-type")
		return serveReq(s, req)
	}

	// Answered before routing and authentication.
	rec := preflight("https://feat.preview.example.com")
	h := rec.Header()
	if rec.Code != 204 || h.Get("Access-Control-Allow-Origin") != "https://feat.preview.example.com" ||
		h.Get("Access-Control-Allow-Methods") != "GET,POST" || h.Get("Access-Control-Allow-Headers") != "authorization,content-type" ||
		h.Get("Access-Control-Allow-Credentials") != "true" || h.Get("Access-Control-Max-Age") != "600" {
		t.Errorf("preflight: %d %v", rec.Code, h)
	}
	if got := preflight("https://evil.example.net").Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("disallowed origin got %q", got)
	}

	req := httptest.NewRequest("GET", "/health", nil)
	req.Header.Set("Origin", "https://app.example.com")
	h = serveReq(s, req).Header()
	if h.Get("Access-Control-Allow-Origin") != "https://app.example.com" || h.Get("Access-Control-Expose-Headers") != "X-Request-Id" ||
		!strings.Contains(strings.Join(h.Values("Vary"), ","), "Origin") {
		t.Errorf("actual request: %v", h)
	}
}
