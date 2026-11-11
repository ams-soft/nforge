package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ams-soft/nforge/app"
	"github.com/ams-soft/nforge/config"
	nhttp "github.com/ams-soft/nforge/http"
)

const htmlAccept = "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"

// start loads the real config.yaml and starts the application on a free port.
func start(t *testing.T, src sources) *nhttp.Server {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	env := filepath.Join(t.TempDir(), "test.env")
	text := fmt.Sprintf("LOGGING_OUTPUT_TERMINAL_ENABLED=false\nHTTP_SERVER_HEADER=false\nHTTP_SERVER_PORT=%d\n", port)
	if err := os.WriteFile(env, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFlags("config.yaml", env)
	if err != nil {
		t.Fatal(err)
	}
	a, err := newApp(cfg, src, app.WithLogger(slog.New(slog.DiscardHandler)))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Shutdown(context.Background()) })
	s, _ := app.Value(a, nhttp.Capability)
	return s
}

func get(s *nhttp.Server, method, target string, hdr ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	s.Srv.Handler.ServeHTTP(rec, req)
	return rec
}

func TestExampleServesEverything(t *testing.T) {
	for name, src := range map[string]sources{
		"directories": {dist: "frontend/dist", public: "public"},
		"embedded":    {embed: true},
	} {
		t.Run(name, func(t *testing.T) {
			s := start(t, src)
			html := []string{"Accept", htmlAccept}
			cases := []struct {
				target   string
				hdr      []string
				want     int
				contains string
			}{
				{"/", nil, 200, `id="root"`},
				{"/dashboard", html, 200, `id="root"`},
				{"/users/42", html, 200, `id="root"`},
				{"/assets/logo.svg", nil, 200, "<svg"},
				{"/assets/robots.txt", nil, 200, "Disallow"},
				{"/build/app.3f9c1e.js", nil, 200, "location.pathname"},
				{"/favicon.svg", nil, 200, "<svg"},
				{"/api/health", nil, 200, `"status":"ok"`},
				{"/health", html, 200, `"status":"ok"`},
				{"/api/missing", html, 404, ""},
				{"/build/missing.js", html, 404, ""},
				{"/assets/missing.svg", html, 404, ""},
				{"/dashboard", nil, 404, ""}, // not a browser navigation
				{"/.env", html, 404, ""},
			}
			for _, c := range cases {
				rec := get(s, "GET", c.target, c.hdr...)
				if rec.Code != c.want || !strings.Contains(rec.Body.String(), c.contains) {
					t.Errorf("GET %s = %d %q, want %d containing %q", c.target, rec.Code, rec.Body, c.want, c.contains)
				}
			}
			if got := get(s, "GET", "/build/app.3f9c1e.js").Header().Get("Cache-Control"); got != nhttp.ImmutableCacheControl {
				t.Errorf("hashed bundle Cache-Control = %q", got)
			}
			if got := get(s, "GET", "/").Header().Get("Cache-Control"); got != "no-cache" {
				t.Errorf("index Cache-Control = %q", got)
			}
			// another method never gets the application (the router answers 404 or 405)
			if rec := get(s, "POST", "/api/health", html...); rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed || strings.Contains(rec.Body.String(), `id="root"`) {
				t.Errorf("POST /api/health = %d %q", rec.Code, rec.Body)
			}
		})
	}
}

func TestMissingFrontendFailsTheStart(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	env := filepath.Join(t.TempDir(), "test.env")
	if err := os.WriteFile(env, fmt.Appendf(nil, "LOGGING_OUTPUT_TERMINAL_ENABLED=false\nHTTP_SERVER_PORT=%d\n", port), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFlags("config.yaml", env)
	if err != nil {
		t.Fatal(err)
	}
	a, err := newApp(cfg, sources{dist: t.TempDir(), public: "public"}, app.WithLogger(slog.New(slog.DiscardHandler)))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "entry point") {
		_ = a.Shutdown(context.Background())
		t.Fatalf("Start = %v, want the missing entry point", err)
	}
}
