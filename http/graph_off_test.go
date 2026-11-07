//go:build !nforge_graph

package nhttp

import (
	"log/slog"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Without the tag an enabled route must fail at startup, never silently vanish.
func TestGraphEnabledWithoutTheBuildTagPanics(t *testing.T) {
	defer func() {
		if r, _ := recover().(string); !strings.Contains(r, "-tags nforge_graph") {
			t.Fatalf("panic = %q", r)
		}
	}()
	var cfg testConfig
	cfg.HTTP.Routes.Graph.Enabled = true
	NewHTTPServer(&testDeps{Config: cfg, Logger: slog.New(slog.DiscardHandler)})
}

func TestGraphDisabledWithoutTheBuildTag(t *testing.T) {
	s := NewHTTPServer(&testDeps{Logger: slog.New(slog.DiscardHandler)})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(stdhttp.MethodGet, "/graph", nil) //nolint:noctx // no context matters here
	s.Srv.Handler.ServeHTTP(rec, req)
	if rec.Code != stdhttp.StatusNotFound {
		t.Fatalf("status = %d", rec.Code)
	}
}
