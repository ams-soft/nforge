package nhttp

import (
	"errors"
	"io"
	"log/slog"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ams-soft/nforge/codec"
	"github.com/ams-soft/nforge/config"
	"github.com/ams-soft/nforge/deps"
	"github.com/ams-soft/nforge/telemetry"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func limitsServer(t *testing.T, mutate func(*config.Config)) *ServerHttp {
	t.Helper()
	var cfg config.Config
	cfg.App = config.AppConfig{Name: "t", Version: "1.0.0"}
	if mutate != nil {
		mutate(&cfg)
	}
	return NewHTTPServer(deps.Deps{Config: cfg, Logger: *slog.New(slog.DiscardHandler)})
}

// Every request context has a deadline, so a handler that honors ctx (and the
// queries it starts) stops instead of outliving the connection (G-35).
func TestRequestContextHasADeadline(t *testing.T) {
	for name, tc := range map[string]struct {
		set  time.Duration
		want time.Duration
	}{
		"default":    {0, 30 * time.Second},
		"configured": {2 * time.Second, 2 * time.Second},
	} {
		t.Run(name, func(t *testing.T) {
			s := limitsServer(t, func(c *config.Config) { c.HTTP.Server.HandlerTimeout = tc.set })
			var got time.Duration
			var ok bool
			s.Router().Get("/ctx", func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
				var dl time.Time
				dl, ok = r.Context().Deadline()
				got = time.Until(dl)
				w.WriteHeader(stdhttp.StatusNoContent)
			})
			serveReq(s, httptest.NewRequest("GET", "/ctx", nil))
			if !ok || got > tc.want || got < tc.want-5*time.Second {
				t.Fatalf("deadline in %v (set=%v), want about %v", got, ok, tc.want)
			}
		})
	}
}

func TestRequestContextIsCanceledAtTheDeadline(t *testing.T) {
	s := limitsServer(t, func(c *config.Config) { c.HTTP.Server.HandlerTimeout = 20 * time.Millisecond })
	var err error
	s.Router().Get("/slow", func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		select {
		case <-r.Context().Done():
			err = r.Context().Err()
		case <-time.After(5 * time.Second):
		}
	})
	serveReq(s, httptest.NewRequest("GET", "/slow", nil))
	if err == nil {
		t.Fatal("a handler waiting on ctx must be released at the deadline")
	}
}

// A raw route that reads r.Body itself is bounded too (G-12).
func TestRawRouteBodyIsBounded(t *testing.T) {
	read := func(s *ServerHttp, body string) (int, error) {
		var n int
		var err error
		s.Router().Post("/raw", func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
			var b []byte
			b, err = io.ReadAll(r.Body)
			n = len(b)
			w.WriteHeader(stdhttp.StatusNoContent)
		})
		serveReq(s, httptest.NewRequest("POST", "/raw", strings.NewReader(body)))
		return n, err
	}

	t.Run("configured", func(t *testing.T) {
		s := limitsServer(t, func(c *config.Config) { c.HTTP.Server.MaxBodyBytes = 1024 })
		if n, err := read(s, strings.Repeat("a", 1000)); err != nil || n != 1000 {
			t.Fatalf("a body under the limit: %d %v", n, err)
		}
	})
	t.Run("over the limit", func(t *testing.T) {
		s := limitsServer(t, func(c *config.Config) { c.HTTP.Server.MaxBodyBytes = 1024 })
		_, err := read(s, strings.Repeat("a", 5000))
		var tooBig *stdhttp.MaxBytesError
		if !errors.As(err, &tooBig) {
			t.Fatalf("want *http.MaxBytesError, got %v", err)
		}
	})
	t.Run("defaults to the codec limit", func(t *testing.T) {
		s := limitsServer(t, func(c *config.Config) { c.Codec.MaxPayloadBytes = 2048 })
		_, err := read(s, strings.Repeat("a", 4096))
		var tooBig *stdhttp.MaxBytesError
		if !errors.As(err, &tooBig) {
			t.Fatalf("want *http.MaxBytesError at the codec limit, got %v", err)
		}
	})
}

// A typed route answers 413 whichever limit trips first: the codec's own (the
// body limit sits one byte above it so the codec still sees the excess) or a
// smaller http.server.max_body_bytes.
func TestTypedRouteAnswers413AtEitherLimit(t *testing.T) {
	post := func(s *ServerHttp, size int) *httptest.ResponseRecorder {
		s.Typed().TPost("/items", item{}, item{}, func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
			_ = SendJSON(w, r, stdhttp.StatusOK, Body[item](r))
		})
		req := httptest.NewRequest("POST", "/items", strings.NewReader(`{"name":"`+strings.Repeat("x", size)+`"}`))
		req.Header.Set("Content-Type", "application/json")
		return serveReq(s, req)
	}
	restore := codec.Default()
	t.Cleanup(func() { codec.SetDefault(restore) })

	t.Run("codec limit", func(t *testing.T) {
		reg, err := codec.New(codec.Config{MaxPayloadBytes: 64})
		if err != nil {
			t.Fatal(err)
		}
		codec.SetDefault(reg)
		s := limitsServer(t, func(c *config.Config) { c.Codec.MaxPayloadBytes = 64 })
		if rec := post(s, 200); rec.Code != stdhttp.StatusRequestEntityTooLarge {
			t.Fatalf("status %d, want 413: %s", rec.Code, rec.Body.String())
		}
		if rec := post(limitsServer(t, func(c *config.Config) { c.Codec.MaxPayloadBytes = 64 }), 20); rec.Code != stdhttp.StatusOK {
			t.Fatalf("a body under the limit: %d %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("max_body_bytes below the codec limit", func(t *testing.T) {
		reg, err := codec.New(codec.Config{MaxPayloadBytes: 1 << 20})
		if err != nil {
			t.Fatal(err)
		}
		codec.SetDefault(reg)
		s := limitsServer(t, func(c *config.Config) { c.HTTP.Server.MaxBodyBytes = 64 })
		if rec := post(s, 200); rec.Code != stdhttp.StatusRequestEntityTooLarge {
			t.Fatalf("status %d, want 413: %s", rec.Code, rec.Body.String())
		}
	})
}

// Private responses must not be stored by shared caches, and the envelope
// must not be MIME-sniffed (G-39).
func TestPrivateResponsesAreNoStoreAndNosniff(t *testing.T) {
	s := newSecurityServer(t, func(c *config.Config) {})
	rec := serveReq(s, httptest.NewRequest("POST", "/private", nil))
	if rec.Code != stdhttp.StatusUnauthorized {
		t.Fatalf("status %d", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control on a private route's 401 = %q, want no-store", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}

	// a public route is not forced out of caches
	rec = serveReq(s, httptest.NewRequest("GET", "/health", nil))
	if got := rec.Header().Get("Cache-Control"); got != "" {
		t.Errorf("a public route must not be forced to no-store, got %q", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("health X-Content-Type-Options = %q", got)
	}
}

type item struct {
	Name string `json:"name"`
}

// tracing.otel.trust_incoming wires through to the middleware (G-17).
func TestTracingTrustIncomingFollowsTheConfig(t *testing.T) {
	tp := sdktrace.NewTracerProvider(sdktrace.WithIDGenerator(telemetry.IDGenerator()))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev); _ = tp.Shutdown(t.Context()) })

	const forged = "33333333333333333333333333333333"
	for _, trust := range []bool{true, false} {
		s := limitsServer(t, func(c *config.Config) {
			c.Logging.Output.Otel.Enabled = true // tracing is on when OTel logging is
			c.Tracing.Otel.TrustIncoming = &trust
		})
		s.Router().Get("/id", func(w stdhttp.ResponseWriter, r *stdhttp.Request) { w.WriteHeader(stdhttp.StatusNoContent) })
		req := httptest.NewRequest("GET", "/id", nil)
		req.Header.Set("X-Request-Id", forged)
		rec := serveReq(s, req)
		adopted := rec.Header().Get("X-Request-Id") == forged
		if adopted != trust {
			t.Errorf("trust_incoming=%v: X-Request-Id %q adopted=%v", trust, rec.Header().Get("X-Request-Id"), adopted)
		}
	}
}

// The spec endpoints are public: rendering walks every route type, so it is
// done once per route set and conditional requests are answered with 304 (G-11).
func TestSpecIsCachedAndConditional(t *testing.T) {
	s := newSecurityServer(t, func(c *config.Config) {})
	get := func(path, etag string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		if etag != "" {
			req.Header.Set("If-None-Match", etag)
		}
		return serveReq(s, req)
	}
	for _, path := range []string{"/docs/swagger.json", "/docs/swagger.yaml"} {
		first := get(path, "")
		if first.Code != stdhttp.StatusOK || first.Header().Get("ETag") == "" {
			t.Fatalf("%s: %d etag=%q", path, first.Code, first.Header().Get("ETag"))
		}
		again := get(path, "")
		if again.Header().Get("ETag") != first.Header().Get("ETag") || again.Body.String() != first.Body.String() {
			t.Errorf("%s: a second request must return the same document", path)
		}
		if cached := get(path, first.Header().Get("ETag")); cached.Code != stdhttp.StatusNotModified || cached.Body.Len() != 0 {
			t.Errorf("%s: If-None-Match must answer 304 without a body, got %d", path, cached.Code)
		}
		if stale := get(path, `"other"`); stale.Code != stdhttp.StatusOK {
			t.Errorf("%s: a different ETag must get the document, got %d", path, stale.Code)
		}
	}

	// a route added later (what AddDocRoute does) invalidates the cache
	before := get("/docs/swagger.json", "")
	s.TypedRoutes = append(s.TypedRoutes, TypedRoute{Method: "GET", Path: "/added/later", Codec: "application/json"})
	after := get("/docs/swagger.json", "")
	if after.Header().Get("ETag") == before.Header().Get("ETag") || !strings.Contains(after.Body.String(), "/added/later") {
		t.Error("the spec must include a route registered after it was first served")
	}
}
