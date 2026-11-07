package nhttp

import (
	"log/slog"
	"testing"
	"time"
)

func TestNewHTTPServerTimeouts(t *testing.T) {
	// A Config built by hand (no Load) still gets the default timeouts.
	var cfg testConfig
	cfg.HTTP.Server.Port = 8080
	cfg.HTTP.Server.WriteTimeout = 2 * time.Minute
	s := NewHTTPServer(&testDeps{Config: cfg, Logger: slog.New(slog.DiscardHandler)})

	if s.Srv.Addr != ":8080" {
		t.Errorf("Addr = %q", s.Srv.Addr)
	}
	if s.Srv.ReadHeaderTimeout != 10*time.Second || s.Srv.ReadTimeout != 30*time.Second ||
		s.Srv.WriteTimeout != 2*time.Minute || s.Srv.IdleTimeout != 120*time.Second {
		t.Errorf("timeouts: header=%s read=%s write=%s idle=%s",
			s.Srv.ReadHeaderTimeout, s.Srv.ReadTimeout, s.Srv.WriteTimeout, s.Srv.IdleTimeout)
	}
	// The defaults are applied to a copy: the caller's Config is unchanged.
	if cfg.HTTP.Server.ReadTimeout != 0 {
		t.Errorf("the caller's Config was mutated: %+v", cfg.HTTP.Server)
	}
}
