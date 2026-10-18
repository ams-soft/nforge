package config

import (
	"fmt"
	"time"
)

const (
	defaultReadHeaderTimeout = 10 * time.Second
	defaultReadTimeout       = 30 * time.Second
	defaultWriteTimeout      = 30 * time.Second
	defaultIdleTimeout       = 120 * time.Second
)

// ApplyServerDefaults fills the http.server timeouts left unset (zero). Load
// calls it; nhttp.NewHTTPServer calls it again so a Config built by hand
// (tests, embedding) still gets a server with timeouts. There is deliberately
// no way to disable them: net/http treats zero as "no timeout", the unsafe
// default this exists to avoid.
func ApplyServerDefaults(s *ServerConfig) {
	setDuration(&s.ReadHeaderTimeout, defaultReadHeaderTimeout)
	setDuration(&s.ReadTimeout, defaultReadTimeout)
	setDuration(&s.WriteTimeout, defaultWriteTimeout)
	setDuration(&s.IdleTimeout, defaultIdleTimeout)
}

func setDuration(d *time.Duration, def time.Duration) {
	if *d == 0 {
		*d = def
	}
}

// validateServer rejects negative timeouts (net/http would read them as
// already expired) and a header timeout longer than the whole-request one,
// which could never apply.
func validateServer(s ServerConfig) error {
	timeouts := []struct {
		field string
		value time.Duration
	}{
		{"read_header_timeout", s.ReadHeaderTimeout},
		{"read_timeout", s.ReadTimeout},
		{"write_timeout", s.WriteTimeout},
		{"idle_timeout", s.IdleTimeout},
	}
	for _, t := range timeouts {
		if t.value < 0 {
			return fmt.Errorf("http.server.%s must be positive, got %s", t.field, t.value)
		}
	}
	if s.ReadHeaderTimeout > s.ReadTimeout {
		return fmt.Errorf("http.server.read_header_timeout (%s) must not exceed http.server.read_timeout (%s)", s.ReadHeaderTimeout, s.ReadTimeout)
	}
	return nil
}
