package nhttp

import (
	"log/slog"
	"testing"
	"time"

	"github.com/ams-soft/nforge/config"
	"github.com/ams-soft/nforge/deps"
)

func TestNewHTTPServerTimeouts(t *testing.T) {
	// A Config built by hand (no config.Load) still gets the default timeouts.
	var cfg config.Config
	cfg.HTTP.Server.Port = 8080
	cfg.HTTP.Server.WriteTimeout = 2 * time.Minute
	s := NewHTTPServer(deps.Deps{Config: cfg, Logger: *slog.New(slog.DiscardHandler)})

	if s.Srv.Addr != ":8080" {
		t.Errorf("Addr = %q", s.Srv.Addr)
	}
	if s.Srv.ReadHeaderTimeout != 10*time.Second || s.Srv.ReadTimeout != 30*time.Second ||
		s.Srv.WriteTimeout != 2*time.Minute || s.Srv.IdleTimeout != 120*time.Second {
		t.Errorf("timeouts: header=%s read=%s write=%s idle=%s",
			s.Srv.ReadHeaderTimeout, s.Srv.ReadTimeout, s.Srv.WriteTimeout, s.Srv.IdleTimeout)
	}
	// The defaults are applied to a copy: Deps.Config keeps what the app passed.
	if s.Deps.Config.HTTP.Server.ReadTimeout != 0 {
		t.Errorf("Deps.Config mutated: %+v", s.Deps.Config.HTTP.Server)
	}
}
