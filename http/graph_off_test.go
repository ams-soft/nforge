//go:build !nforge_graph

package nhttp

import (
	"log/slog"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ams-soft/nforge/config"
	"github.com/ams-soft/nforge/deps"
)

// Without the tag an enabled route must fail at startup, never silently vanish.
func TestGraphEnabledWithoutTheBuildTagPanics(t *testing.T) {
	defer func() {
		if r, _ := recover().(string); !strings.Contains(r, "-tags nforge_graph") {
			t.Fatalf("panic = %q", r)
		}
	}()
	var cfg config.Config
	cfg.HTTP.Routes.Graph.Enabled = true
	NewHTTPServer(&deps.Deps{Config: cfg, Logger: slog.New(slog.DiscardHandler)})
}

func TestGraphDisabledWithoutTheBuildTag(t *testing.T) {
	s := NewHTTPServer(&deps.Deps{Logger: slog.New(slog.DiscardHandler)})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(stdhttp.MethodGet, "/graph", nil) //nolint:noctx // no context matters here
	s.Srv.Handler.ServeHTTP(rec, req)
	if rec.Code != stdhttp.StatusNotFound {
		t.Fatalf("status = %d", rec.Code)
	}
}
