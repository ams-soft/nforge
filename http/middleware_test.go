package nhttp

import (
	"bytes"
	"encoding/json"
	"log/slog"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	router "github.com/ams-soft/go-ams-router"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestLoggerPatternAndLevel(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	r := router.NewRouter()
	r.Use(Logger(logger))
	r.Route("/api", func(api router.Router) {
		api.Get("/users/{id:int}", func(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
			w.WriteHeader(stdhttp.StatusTeapot)
		})
	})

	cases := []struct {
		target, pattern, level string
		status                 int
	}{
		{"/api/users/7", "/api/users/{id:int}", "WARN", 418},
		{"/missing", "", "WARN", 404},
	}
	for _, c := range cases {
		buf.Reset()
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", c.target, nil))
		var rec map[string]any
		if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
			t.Fatalf("%s: %v: %s", c.target, err, buf.String())
		}
		if rec["pattern"] != c.pattern || rec["level"] != c.level || rec["status"] != float64(c.status) || rec["path"] != c.target {
			t.Errorf("%s: %v", c.target, rec)
		}
	}
}

func TestTracingSpanNameAndOriginalRequestID(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr)))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	r := router.NewRouter()
	r.Use(Tracing())
	r.Route("/api", func(api router.Router) {
		api.Get("/users/{id}", func(stdhttp.ResponseWriter, *stdhttp.Request) {})
	})

	cases := []struct {
		requestID    string
		wantOriginal string
	}{
		{"client-id.1", "client-id.1"},
		{"bad\nid", ""},
		{strings.Repeat("a", 65), ""},
	}
	for _, c := range cases {
		req := httptest.NewRequest("GET", "/api/users/7", nil)
		req.Header.Set("X-Request-Id", c.requestID)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		spans := sr.Ended()
		span := spans[len(spans)-1]
		if span.Name() != "GET /api/users/{id}" {
			t.Errorf("span name %q", span.Name())
		}
		attrs := map[string]string{}
		for _, a := range span.Attributes() {
			attrs[string(a.Key)] = a.Value.String()
		}
		if attrs["http.route"] != "/api/users/{id}" || attrs["request_id"] != span.SpanContext().TraceID().String() {
			t.Errorf("attributes %v", attrs)
		}
		if attrs["request_id.original"] != c.wantOriginal {
			t.Errorf("%q: request_id.original = %q", c.requestID, attrs["request_id.original"])
		}
		if rec.Header().Get("X-Request-Id") != span.SpanContext().TraceID().String() {
			t.Errorf("response X-Request-Id %q", rec.Header().Get("X-Request-Id"))
		}
	}
}
