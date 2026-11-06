package nhttp

import (
	"context"
	"log/slog"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ams-soft/nforge/codec"
	"github.com/ams-soft/nforge/config"
	"github.com/ams-soft/nforge/deps"
	"go.opentelemetry.io/otel"
)

type isolatedBody struct {
	Name string `json:"name"`
}

func isolatedServer(t *testing.T, name string, formats []string, limit config.ByteSize) *ServerHttp {
	t.Helper()
	var cfg config.Config
	cfg.Env, cfg.App.Name, cfg.App.Version = "local", name, "1"
	cfg.Codec.Formats.Enabled, cfg.Codec.Default, cfg.Codec.MaxPayloadBytes = formats, "json", limit
	cfg.Logging.Output.Otel.Enabled = true
	cfg.Logging.Output.Otel.Endpoint, cfg.Logging.Output.Otel.Insecure = "127.0.0.1:1", true
	cfg.Tracing.Otel.Endpoint, cfg.Tracing.Otel.Insecure = "127.0.0.1:1", true
	d, err := deps.Bootstrap(context.Background(), cfg, deps.WithoutGlobals())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 1)
		defer cancel()
		_ = d.Shutdown(ctx)
	})
	s := NewHTTPServer(d)
	Post(s.Typed(), "/echo", func(_ *stdhttp.Request, in isolatedBody) (isolatedBody, error) { return in, nil })
	return s
}

// Two applications in one process keep their own limits and leave every
// process-wide default alone.
func TestIsolatedServersCoexist(t *testing.T) {
	registry, logger, tracer := codec.Default(), slog.Default(), otel.GetTracerProvider()

	small := isolatedServer(t, "small", []string{"json"}, 32)
	large := isolatedServer(t, "large", []string{"json", "xml"}, 1<<20)

	if codec.Default() != registry || slog.Default() != logger || otel.GetTracerProvider() != tracer {
		t.Fatal("WithoutGlobals changed a process default")
	}
	if small.Deps.Codec == large.Deps.Codec || small.Deps.TracerProvider == large.Deps.TracerProvider {
		t.Fatal("each server must own its resources")
	}
	call := func(s *ServerHttp, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(context.Background(), stdhttp.MethodPost, "/echo", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		s.Srv.Handler.ServeHTTP(rec, req)
		return rec
	}
	payload := `{"name":"` + strings.Repeat("a", 64) + `"}`
	if rec := call(small, payload); rec.Code != stdhttp.StatusRequestEntityTooLarge {
		t.Fatalf("small: %d %s", rec.Code, rec.Body)
	}
	rec := call(large, payload)
	if rec.Code != stdhttp.StatusOK {
		t.Fatalf("large: %d %s", rec.Code, rec.Body)
	}
	if rec.Header().Get("X-Request-Id") == "" {
		t.Fatal("tracing must work on the server's own provider")
	}
}

// A route codec is checked against the server's registry, not the process one.
func TestIsolatedRegistrationUsesTheServerRegistry(t *testing.T) {
	s := isolatedServer(t, "json-only", []string{"json"}, 1<<20)
	defer func() {
		if r, _ := recover().(string); !strings.Contains(r, "route codec") {
			t.Fatalf("panic = %q", r)
		}
	}()
	Get(s.Typed(), "/x", func(*stdhttp.Request, Empty) (Empty, error) { return Empty{}, nil }, Opts{Codec: "xml"})
}
