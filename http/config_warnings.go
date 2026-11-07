package nhttp

import (
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/ams-soft/nforge/cache"
	"github.com/ams-soft/nforge/database"
	"github.com/ams-soft/nforge/module"
)

// finding is one weakening setting. exception names the prd_exceptions key
// that accepts it in production; empty means it is only ever a warning.
type finding struct{ exception, msg string }

// warnings reports the settings of the section that are valid but weaken the
// deployment (module.Source.Warn turns them into errors in prd unless
// prd_exceptions accepts them).
func warnings(cfg *Config, app module.App) []finding {
	var all []finding
	all = append(all, keyFileWarnings(cfg)...)
	all = append(all, docsWarnings(cfg, app)...)
	all = append(all, graphWarnings(cfg, app)...)
	return all
}

// edgeTLSWarnings flags a server that terminates TLS (HTTPS or HTTP/3) while
// its backends are reached without it: the operator cared about the edge but
// the data and credentials behind it travel in clear text. A Unix socket is
// exempt (TLS does not apply to it).
func edgeTLSWarnings(cfg *Config, c *cache.Config, d *database.Config) []finding {
	s := cfg.Server
	if !s.TLS.Enabled && !s.HTTP3.Enabled {
		return nil
	}
	var w []finding
	if c != nil && c.Enabled && c.Driver == cache.DriverRedis && !c.UsesTLS() {
		w = append(w, finding{"redis_plaintext", "the server uses HTTPS or HTTP/3 but cache.redis is reached without TLS; use rediss:// or cache.redis.tls.enabled"})
	}
	if d != nil && d.Enabled && !d.RequiresTLS() {
		w = append(w, finding{"database_plaintext", "the server uses HTTPS or HTTP/3 but the database connection does not require TLS (database.ssl.mode must be require, verify-ca or verify-full)"})
	}
	return w
}

// docsWarnings flags the docs, health and readiness endpoints in production.
// They are unauthenticated by design (a browser cannot send a bearer token on
// navigation, and probes carry none): the docs page and the spec list every
// route, auth profile and scope, and readiness reports the state of the
// database, cache and JOSE. It stays a warning, because many deployments keep
// them behind a gateway or on a private network; app.internal_use silences it.
func docsWarnings(cfg *Config, app module.App) []finding {
	if app.Env != "prd" || app.InternalUse {
		return nil
	}
	var on []string
	if cfg.Docs.Enabled {
		on = append(on, "http.docs")
	}
	if cfg.Routes.Health.Enabled {
		on = append(on, "http.routes.health")
	}
	if cfg.Routes.Readiness.Enabled {
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
func graphWarnings(cfg *Config, app module.App) []finding {
	if app.Env != "prd" || app.InternalUse || !cfg.Routes.Graph.Enabled {
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
	s := cfg.Server
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
