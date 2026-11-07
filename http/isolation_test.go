package nhttp

import (
	"context"
	"fmt"
	"log/slog"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ams-soft/nforge/app"
	"github.com/ams-soft/nforge/codec"
	"github.com/ams-soft/nforge/config"
	"github.com/ams-soft/nforge/internal/apptest"
	"github.com/ams-soft/nforge/logs"
	"github.com/ams-soft/nforge/module"
	"github.com/ams-soft/nforge/telemetry"
	"go.opentelemetry.io/otel"
)

type isolatedBody struct {
	Name string `json:"name"`
}

func isolatedServer(t *testing.T, name string, formats []string, limit codec.ByteSize) *Server {
	t.Helper()
	yaml := fmt.Sprintf(`app: {name: %s, version: '1'}
codec: {formats: {enabled: [%s]}, default: json, max_payload_bytes: %d}
logging: {output: {otel: {enabled: true, endpoint: '127.0.0.1:1', insecure: true}}}
tracing: {otel: {endpoint: '127.0.0.1:1', insecure: true}}
http: {server: {host: 127.0.0.1, port: %d}}
`, name, strings.Join(formats, ", "), int64(limit), freePort(t))
	doc, err := config.LoadFlags(writeConfig(t, yaml), "")
	if err != nil {
		t.Fatal(err)
	}
	var s *Server
	api := module.New("api", module.Definition{Requires: []module.Handle{Capability}, Init: func(_ context.Context, sc *module.Scope) (module.Hooks, error) {
		s = module.Require(sc, Capability)
		Post(s.Typed(), "/echo", func(_ *stdhttp.Request, in isolatedBody) (isolatedBody, error) { return in, nil })
		return module.Hooks{}, nil
	}})
	a, err := app.New(doc, app.Modules(logs.Module(), telemetry.Module(), codec.Module(), Module(), api), apptest.Quiet())
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
		defer cancel()
		_ = a.Shutdown(ctx)
	})
	return s
}

// Two applications in one process keep their own limits and leave every
// process-wide default alone.
func TestIsolatedServersCoexist(t *testing.T) {
	registry, logger, tracer := codec.Default(), slog.Default(), otel.GetTracerProvider()

	small := isolatedServer(t, "small", []string{"json"}, 32)
	large := isolatedServer(t, "large", []string{"json", "xml"}, 1<<20)

	if codec.Default() != registry || slog.Default() != logger || otel.GetTracerProvider() != tracer {
		t.Fatal("an application without app.WithProcessGlobals changed a process default")
	}
	if small.codec == large.codec || small.tracing.Provider == large.tracing.Provider {
		t.Fatal("each server must own its resources")
	}
	call := func(s *Server, body string) *httptest.ResponseRecorder {
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
