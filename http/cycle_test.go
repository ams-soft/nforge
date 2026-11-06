package nhttp

import (
	"log/slog"
	stdhttp "net/http"
	"net/http/httptest"
	"testing"

	"github.com/ams-soft/nforge/config"
	"github.com/ams-soft/nforge/deps"
)

type cycleResponse struct {
	Name string         `json:"name" xml:"name" codec:"required"`
	Next *cycleResponse `json:"next" xml:"next"`
}

// A handler that returns a value containing itself used to crash the whole
// process on CBOR and XML routes (stack overflow, not recoverable). It must be
// a 500 on every codec.
func TestTypedRouteWithCyclicResponseIsA500(t *testing.T) {
	var cfg config.Config
	cfg.App = config.AppConfig{Name: "t", Version: "1.0.0"}
	cfg.Codec.Formats.Enabled = []string{"json", "cbor", "xml"}
	s := NewHTTPServer(deps.Deps{Config: cfg, Logger: *slog.New(slog.DiscardHandler)})
	for _, format := range []string{"json", "cbor", "xml"} {
		s.Typed().TGet("/cycle-"+format, nil, cycleResponse{}, func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
			a := &cycleResponse{Name: "a"}
			a.Next = a
			_ = SendJSON(w, r, stdhttp.StatusOK, a)
		}, Opts{Codec: format})
	}
	for _, format := range []string{"json", "cbor", "xml"} {
		rec := httptest.NewRecorder()
		s.Srv.Handler.ServeHTTP(rec, httptest.NewRequest(stdhttp.MethodGet, "/cycle-"+format, nil))
		if rec.Code != stdhttp.StatusInternalServerError {
			t.Errorf("%s: status %d, want 500", format, rec.Code)
		}
	}
}
