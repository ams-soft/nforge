package config

import (
	"fmt"
	"net/url"
	"os"
	"runtime"
	"strings"
)

// transportWarnings reports settings that are valid but weaken the deployment
// and that a hard error would break in legitimate setups (a key mounted 0644
// by an orchestrator, a database reached through a trusted private network).
// deps.NewDeps logs them at startup.
func transportWarnings(cfg *Config) []string {
	var w []string
	w = append(w, keyFileWarnings(cfg)...)
	w = append(w, redisWarnings(cfg)...)
	w = append(w, databaseWarnings(cfg)...)
	w = append(w, docsWarnings(cfg)...)
	return w
}

// docsWarnings flags the documentation endpoints in production. They are
// public by design (a browser cannot send a bearer token on navigation), and
// they list every route with its auth profile and scopes and the names of the
// Go packages the schemas come from.
func docsWarnings(cfg *Config) []string {
	if cfg.Env == "prd" && cfg.HTTP.Docs.Enabled {
		return []string{"http.docs is enabled in prd: the page and the OpenAPI spec are public and list every route, auth profile and scope; disable them or restrict the path at the gateway"}
	}
	return nil
}

// keyFileWarnings flags TLS private keys that group or others can read, the
// check jose applies to its own key files (and enforces).
func keyFileWarnings(cfg *Config) []string {
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
	var w []string
	for _, f := range files {
		info, err := os.Stat(f.path)
		if err != nil || info.IsDir() {
			continue // ValidateServer already reported unreadable pairs
		}
		if perm := info.Mode().Perm(); perm&0o077 != 0 {
			w = append(w, fmt.Sprintf("%s is readable by group or others (mode %04o); restrict the private key to its owner (chmod 600)", f.field, perm))
		}
	}
	return w
}

// redisWarnings flags Redis credentials that would travel in clear text to a
// host that is not this machine.
func redisWarnings(cfg *Config) []string {
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
		return []string{"cache.redis sends its credentials without TLS to a host that is not local; use rediss:// or cache.redis.tls.enabled"}
	}
	return nil
}

// insecureSSLModes are the libpq sslmodes that can send the password unencrypted.
var insecureSSLModes = []string{"", "disable", "allow", "prefer"}

// databaseWarnings flags a PostgreSQL connection to a non-local host whose
// sslmode can fall back to clear text (the pgx default, "prefer", does).
func databaseWarnings(cfg *Config) []string {
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
			return []string{fmt.Sprintf("database.ssl.mode is %s for a non-local host: the password may be sent unencrypted; use verify-full", shown)}
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
