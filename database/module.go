package database

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/ams-soft/nforge/module"
)

// Capability is the application's database, published by Module. It is nil
// for nobody: a disabled database module publishes nothing, so consumers
// declare it in Requires (cannot run without) or Optional (check Lookup's ok).
var Capability = module.NewCapability[*DB]("database")

const startupTimeout = 15 * time.Second

// Validate checks what can be checked without connecting: the connection
// fields, the pool values and, when enabled, the tenancy section. It reads no
// file and dials nothing.
func (c Config) Validate() error {
	if err := c.Tenancy.Validate(); err != nil {
		return err
	}
	if _, err := c.ConnectionString(); err != nil {
		return err
	}
	if err := validateNonNegative(c); err != nil {
		return err
	}
	if c.Pool.MinConns > 0 && c.Pool.MaxConns > 0 && c.Pool.MinConns > c.Pool.MaxConns {
		return fmt.Errorf("database.pool.min_conns (%d) must not exceed database.pool.max_conns (%d)", c.Pool.MinConns, c.Pool.MaxConns)
	}
	return nil
}

// secureSSLModes are the libpq sslmodes that never fall back to clear text.
var secureSSLModes = []string{"require", "verify-ca", "verify-full"}

// RequiresTLS reports whether the connection cannot fall back to clear text.
// A Unix socket counts as protected: TLS does not apply to it.
func (c Config) RequiresTLS() bool {
	host, mode, _ := c.endpoint()
	return strings.HasPrefix(host, "/") || slices.Contains(secureSSLModes, mode)
}

// PlaintextRisk reports a password that may be sent unencrypted: a non-local
// host with an sslmode that can fall back to clear text (the pgx default,
// "prefer", does).
func (c Config) PlaintextRisk() (string, bool) {
	host, mode, hasPassword := c.endpoint()
	if !hasPassword || isLocalHost(host) || slices.Contains(secureSSLModes, mode) {
		return "", false
	}
	shown := mode
	if shown == "" {
		shown = "unset (prefer)"
	}
	return fmt.Sprintf("database.ssl.mode is %s for a non-local host: the password may be sent unencrypted; use verify-full", shown), true
}

// endpoint returns the host, sslmode and whether a password is configured,
// from the DSN when it is set.
func (c Config) endpoint() (host, sslmode string, hasPassword bool) {
	if dsn := strings.TrimSpace(c.DSN); dsn != "" {
		return dsnParts(dsn)
	}
	return c.Host, c.SSL.Mode, c.Password != ""
}

// dsnParts extracts host, sslmode and whether a password is present from a URL
// or key=value DSN.
func dsnParts(dsn string) (host, sslmode string, hasPassword bool) {
	if u, err := url.Parse(dsn); err == nil && u.Scheme != "" {
		q := u.Query()
		_, hasUserPassword := u.User.Password()
		return u.Hostname(), q.Get("sslmode"), hasUserPassword || q.Get("password") != ""
	}
	for field := range strings.FieldsSeq(dsn) {
		key, value, ok := strings.Cut(field, "=")
		if !ok {
			continue
		}
		switch key {
		case "host":
			host = value
		case "sslmode":
			sslmode = value
		case "password":
			hasPassword = value != ""
		}
	}
	return host, sslmode, hasPassword
}

// isLocalHost reports whether host is this machine: a loopback name or
// address, or a Unix socket directory.
func isLocalHost(host string) bool {
	if host == "" || strings.HasPrefix(host, "/") || strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// Module is the database module: it owns the database section and, when
// database.enabled is true, opens the PostgreSQL pool at Init (with a startup
// ping and, with tenancy, CheckTenancy; a failure is fatal) and publishes it
// under Capability. Readiness pings the pool; Stop waits for the connections
// to be released, after every module that uses the database stopped.
func Module() module.Descriptor {
	return module.Descriptor{ID: "database", Section: "database", Configure: func(src module.Source) (module.Definition, error) {
		var cfg Config
		if err := src.Decode(&cfg); err != nil {
			return module.Definition{}, err
		}
		if cfg.Enabled {
			if err := cfg.Validate(); err != nil {
				return module.Definition{}, err
			}
			if msg, risky := cfg.PlaintextRisk(); risky {
				src.Warn("database_plaintext", msg)
			}
		}
		return module.Definition{
			Disabled: !cfg.Enabled,
			Provides: []module.Handle{Capability},
			Optional: []module.Handle{module.Logger},
			Config:   cfg,
			Init: func(ctx context.Context, sc *module.Scope) (module.Hooks, error) {
				openCtx, cancel := context.WithTimeout(ctx, startupTimeout)
				defer cancel()
				db, err := Open(openCtx, cfg)
				if err != nil {
					return module.Hooks{}, fmt.Errorf("unable to setup database: %w", err)
				}
				module.Provide(sc, Capability, db)
				if logger, ok := module.Lookup(sc, module.Logger); ok {
					logger.InfoContext(ctx, "[DatabaseModule] Database setup completed", "database", cfg)
				}
				return module.Hooks{Ready: db.Ping, Stop: db.Shutdown}, nil
			},
		}, nil
	}}
}
