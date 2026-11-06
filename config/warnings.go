package config

import (
	"fmt"
	"net/url"
	"os"
	"runtime"
	"slices"
	"strings"

	"github.com/ams-soft/nforge/cache"
)

// finding is one weakening setting. exception names the prd_exceptions key
// that accepts it in production; empty means it is only ever a warning.
type finding struct{ exception, msg string }

// transportWarnings reports settings that are valid but weaken the deployment.
// Outside prd they are warnings (a key mounted 0644 by an orchestrator, a
// database reached through a trusted private network are legitimate there);
// deps.NewDeps logs them at startup. In prd each one is a load error unless
// its prd_exceptions key is set, in which case it stays a warning.
func transportWarnings(cfg *Config) (warnings []string, err error) {
	var all []finding
	all = append(all, keyFileWarnings(cfg)...)
	all = append(all, redisWarnings(cfg)...)
	all = append(all, databaseWarnings(cfg)...)
	all = append(all, docsWarnings(cfg)...)
	all = append(all, graphWarnings(cfg)...)
	all = append(all, edgeTLSWarnings(cfg)...)

	var refused []string
	for _, f := range all {
		if f.exception != "" && cfg.Env == "prd" && !cfg.PrdExceptions.allows(f.exception) {
			refused = append(refused, fmt.Sprintf("%s (accept it with prd_exceptions.%s: true)", f.msg, f.exception))
			continue
		}
		warnings = append(warnings, f.msg)
	}
	if len(refused) > 0 {
		return nil, fmt.Errorf("env prd refuses insecure settings:\n  - %s", strings.Join(refused, "\n  - "))
	}
	return warnings, nil
}

// edgeTLSWarnings flags a server that terminates TLS (HTTPS or HTTP/3) while
// its backends are reached without it: the operator cared about the edge but
// the data and credentials behind it travel in clear text. A Unix socket is
// exempt (TLS does not apply to it).
func edgeTLSWarnings(cfg *Config) []finding {
	s := cfg.HTTP.Server
	if !s.TLS.Enabled && !s.HTTP3.Enabled {
		return nil
	}
	var w []finding
	if c := cfg.Cache; c.Enabled && c.Driver == "redis" && !redisUsesTLS(c) {
		w = append(w, finding{"redis_plaintext", "the server uses HTTPS or HTTP/3 but cache.redis is reached without TLS; use rediss:// or cache.redis.tls.enabled"})
	}
	if d := cfg.Database; d.Enabled {
		host, mode := d.Host, d.SSL.Mode
		if dsn := strings.TrimSpace(d.DSN); dsn != "" {
			host, mode, _ = dsnParts(dsn)
		}
		if !strings.HasPrefix(host, "/") && !slices.Contains([]string{"require", "verify-ca", "verify-full"}, mode) {
			w = append(w, finding{"database_plaintext", "the server uses HTTPS or HTTP/3 but the database connection does not require TLS (database.ssl.mode must be require, verify-ca or verify-full)"})
		}
	}
	return w
}

// redisUsesTLS reports whether the configured Redis connection is encrypted.
func redisUsesTLS(c cache.Config) bool {
	if c.Redis.URL != "" {
		u, err := url.Parse(c.Redis.URL)
		return err == nil && u.Scheme == "rediss"
	}
	return c.Redis.TLS.Enabled
}

// docsWarnings flags the docs, health and readiness endpoints in production.
// They are unauthenticated by design (a browser cannot send a bearer token on
// navigation, and probes carry none): the docs page and the spec list every
// route, auth profile and scope, and readiness reports the state of the
// database, cache and JOSE. It stays a warning, because many deployments keep
// them behind a gateway or on a private network; app.internal_use silences it.
func docsWarnings(cfg *Config) []finding {
	if cfg.Env != "prd" || cfg.App.InternalUse {
		return nil
	}
	var on []string
	if cfg.HTTP.Docs.Enabled {
		on = append(on, "http.docs")
	}
	if cfg.HTTP.Routes.Health.Enabled {
		on = append(on, "http.routes.health")
	}
	if cfg.HTTP.Routes.Readiness.Enabled {
		on = append(on, "http.routes.readiness")
	}
	if len(on) == 0 {
		return nil
	}
	return []finding{{"", strings.Join(on, ", ") + " enabled in prd: these endpoints are unauthenticated (docs and the spec list every route, auth profile and scope; readiness reports the state of the dependencies); restrict them at the gateway, or set app.internal_use: true if the service is not reachable from the internet"}}
}

// graphWarnings flags the module graph page in prd. It never stops the startup,
// and the warning is no access control: the page stays public until the gateway
// or the network restricts it.
func graphWarnings(cfg *Config) []finding {
	if cfg.Env != "prd" || cfg.App.InternalUse || !cfg.HTTP.Routes.Graph.Enabled {
		return nil
	}
	return []finding{{"", "http.routes.graph enabled in prd: this unauthenticated page exposes the module topology and its state; restrict it at the gateway or by network, or set app.internal_use: true if the service is not reachable from the internet"}}
}

// keyFileWarnings flags TLS private keys that group or others can read, the
// check jose applies to its own key files (and enforces).
func keyFileWarnings(cfg *Config) []finding {
	if runtime.GOOS == "windows" {
		return nil // POSIX permission bits are not meaningful there
	}
	s := cfg.HTTP.Server
	var files []struct{ field, path string }
	if s.TLS.Enabled {
		files = append(files, struct{ field, path string }{"http.server.tls.key_file", s.TLS.KeyFile})
	}
	if s.HTTP3.Enabled && !s.HTTP3.SameKeyCert() {
		files = append(files, struct{ field, path string }{"http.server.http3.key_file", s.HTTP3.KeyFile})
	}
	var w []finding
	for _, f := range files {
		info, err := os.Stat(f.path)
		if err != nil || info.IsDir() {
			continue // ValidateServer already reported unreadable pairs
		}
		if perm := info.Mode().Perm(); perm&0o077 != 0 {
			w = append(w, finding{"tls_key_permissions", fmt.Sprintf("%s is readable by group or others (mode %04o); restrict the private key to its owner (chmod 600)", f.field, perm)})
		}
	}
	return w
}

// redisWarnings flags Redis credentials that would travel in clear text to a
// host that is not this machine.
func redisWarnings(cfg *Config) []finding {
	c := cfg.Cache
	if !c.Enabled || c.Driver != "redis" {
		return nil
	}
	host, hasCreds, tls := c.Redis.Host, c.Redis.Username != "" || c.Redis.Password != "", c.Redis.TLS.Enabled
	if c.Redis.URL != "" {
		u, err := url.Parse(c.Redis.URL)
		if err != nil {
			return nil // cache.ValidateConfig rejects it
		}
		host, tls = u.Hostname(), u.Scheme == "rediss"
		_, hasPassword := u.User.Password()
		hasCreds = u.User != nil && (u.User.Username() != "" || hasPassword)
	}
	if hasCreds && !tls && !isLocalHost(host) {
		return []finding{{"redis_plaintext", "cache.redis sends its credentials without TLS to a host that is not local; use rediss:// or cache.redis.tls.enabled"}}
	}
	return nil
}

// insecureSSLModes are the libpq sslmodes that can send the password unencrypted.
var insecureSSLModes = []string{"", "disable", "allow", "prefer"}

// databaseWarnings flags a PostgreSQL connection to a non-local host whose
// sslmode can fall back to clear text (the pgx default, "prefer", does).
func databaseWarnings(cfg *Config) []finding {
	d := cfg.Database
	if !d.Enabled {
		return nil
	}
	host, mode, hasPassword := d.Host, d.SSL.Mode, d.Password != ""
	if dsn := strings.TrimSpace(d.DSN); dsn != "" {
		host, mode, hasPassword = dsnParts(dsn)
	}
	if !hasPassword || isLocalHost(host) {
		return nil
	}
	for _, m := range insecureSSLModes {
		if mode == m {
			shown := mode
			if shown == "" {
				shown = "unset (prefer)"
			}
			return []finding{{"database_plaintext", fmt.Sprintf("database.ssl.mode is %s for a non-local host: the password may be sent unencrypted; use verify-full", shown)}}
		}
	}
	return nil
}

// dsnParts extracts host, sslmode and whether a password is present from a URL
// or key=value DSN.
func dsnParts(dsn string) (host, sslmode string, hasPassword bool) {
	if u, err := url.Parse(dsn); err == nil && u.Scheme != "" {
		q := u.Query()
		_, hasUserPassword := u.User.Password()
		return u.Hostname(), q.Get("sslmode"), hasUserPassword || q.Get("password") != ""
	}
	for _, field := range strings.Fields(dsn) {
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
	return host == "" || strings.HasPrefix(host, "/") || isLoopbackHost(host)
}
