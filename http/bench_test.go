package nhttp

import (
	"context"
	"log/slog"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ams-soft/nforge/codec"
)

type benchUser struct {
	ID   int64  `json:"id"`
	Name string `json:"name" codec:"required"`
}

// BenchmarkTypedPost measures one typed JSON request through the whole
// standard chain (recoverer, registry, body limit, handler timeout, security
// headers), with the application's own Registry as the module provides it.
func BenchmarkTypedPost(b *testing.B) {
	registry, err := codec.New(codec.Config{})
	if err != nil {
		b.Fatal(err)
	}
	s := New(Config{}, Dependencies{Logger: slog.New(slog.DiscardHandler), Codec: registry})
	Post(s.Typed(), "/users", func(_ *stdhttp.Request, u benchUser) (benchUser, error) { return u, nil })
	benchRequests(b, s)
}

// BenchmarkTypedPostDefaultRegistry is the same request on the process
// default Registry (app.WithProcessGlobals), which needs no context value.
func BenchmarkTypedPostDefaultRegistry(b *testing.B) {
	s := New(Config{}, Dependencies{Logger: slog.New(slog.DiscardHandler)})
	Post(s.Typed(), "/users", func(_ *stdhttp.Request, u benchUser) (benchUser, error) { return u, nil })
	benchRequests(b, s)
}

func benchRequests(b *testing.B, s *Server) {
	b.Helper()
	const body = `{"id":7,"name":"Ana"}`
	b.ReportAllocs()
	for b.Loop() {
		req := httptest.NewRequestWithContext(context.Background(), stdhttp.MethodPost, "/users", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		s.Srv.Handler.ServeHTTP(rec, req)
		if rec.Code != stdhttp.StatusOK {
			b.Fatalf("status %d: %s", rec.Code, rec.Body)
		}
	}
}
