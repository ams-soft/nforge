package config

import (
	"os"
	"runtime"
	"strings"
	"testing"
)

func loadWarnings(t *testing.T, yaml string) []string {
	t.Helper()
	cfg, err := LoadFlags(writeConfig(t, baseYAML+yaml), "")
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Warnings
}

func hasWarning(warnings []string, part string) bool {
	for _, w := range warnings {
		if strings.Contains(w, part) {
			return true
		}
	}
	return false
}

// HSTS includeSubDomains pins HTTPS on every subdomain for a year: opt-in (G-24).
func TestHSTSIncludeSubdomainsIsOptIn(t *testing.T) {
	cfg, err := LoadFlags(writeConfig(t, baseYAML+"http: {security: {enabled: true}}\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.HTTP.Security.HSTS.IncludeSubdomains; got == nil || *got {
		t.Fatalf("include_subdomains must default to false, got %v", got)
	}
	cfg, err = LoadFlags(writeConfig(t, baseYAML+"http: {security: {enabled: true, hsts: {include_subdomains: true}}}\n"), "")
	if err != nil || !*cfg.HTTP.Security.HSTS.IncludeSubdomains {
		t.Fatalf("an explicit true must be kept: %v", err)
	}
}

func TestMaxPayloadHasACeiling(t *testing.T) {
	for _, in := range []string{"257mb", "1gb", "100gb"} {
		if _, err := LoadFlags(writeConfig(t, baseYAML+"codec: {max_payload_bytes: "+in+"}\n"), ""); err == nil || !strings.Contains(err.Error(), "max_payload_bytes") {
			t.Errorf("max_payload_bytes %s must be refused: %v", in, err)
		}
	}
	for _, in := range []string{"256mb", "5mb", "1"} {
		if _, err := LoadFlags(writeConfig(t, baseYAML+"codec: {max_payload_bytes: "+in+"}\n"), ""); err != nil {
			t.Errorf("max_payload_bytes %s must be accepted: %v", in, err)
		}
	}
}

func TestPlaintextCredentialWarnings(t *testing.T) {
	cases := []struct {
		name, yaml string
		warn       bool
		part       string
	}{
		{"redis password without tls", "cache: {enabled: true, driver: redis, redis: {host: cache.internal, password: x}}\n", true, "cache.redis"},
		{"redis url with password", "cache: {enabled: true, driver: redis, redis: {url: 'redis://:pw@cache.internal:6379'}}\n", true, "cache.redis"},
		{"rediss is encrypted", "cache: {enabled: true, driver: redis, redis: {url: 'rediss://:pw@cache.internal:6379'}}\n", false, "cache.redis"},
		{"redis tls enabled", "cache: {enabled: true, driver: redis, redis: {host: cache.internal, password: x, tls: {enabled: true}}}\n", false, "cache.redis"},
		{"redis on loopback", "cache: {enabled: true, driver: redis, redis: {host: 127.0.0.1, password: x}}\n", false, "cache.redis"},
		{"redis without credentials", "cache: {enabled: true, driver: redis, redis: {host: cache.internal}}\n", false, "cache.redis"},
		{"database default sslmode", "database: {enabled: true, host: db.internal, port: 5432, name: d, user: u, password: p}\n", true, "database.ssl.mode"},
		{"database prefer in dsn", "database: {enabled: true, dsn: 'postgres://u:p@db.internal/d?sslmode=prefer'}\n", true, "database.ssl.mode"},
		{"database kv dsn", "database: {enabled: true, dsn: 'host=db.internal user=u password=p dbname=d'}\n", true, "database.ssl.mode"},
		{"database verify-full", "database: {enabled: true, host: db.internal, port: 5432, name: d, user: u, password: p, ssl: {mode: verify-full}}\n", false, "database.ssl.mode"},
		{"database require in dsn", "database: {enabled: true, dsn: 'postgres://u:p@db.internal/d?sslmode=require'}\n", false, "database.ssl.mode"},
		{"database on localhost", "database: {enabled: true, host: localhost, port: 5432, name: d, user: u, password: p}\n", false, "database.ssl.mode"},
		{"database unix socket", "database: {enabled: true, dsn: 'host=/var/run/postgresql user=u password=p dbname=d'}\n", false, "database.ssl.mode"},
		{"database without password", "database: {enabled: true, host: db.internal, port: 5432, name: d, user: u}\n", false, "database.ssl.mode"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := hasWarning(loadWarnings(t, c.yaml), c.part); got != c.warn {
				t.Fatalf("warning = %v, want %v", got, c.warn)
			}
		})
	}
}

// Outside prd, a TLS private key that others can read is reported, not
// refused: many orchestrators mount secrets 0644 (G-09).
func TestTLSKeyPermissionWarning(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits")
	}
	cert, key := testPair(t)
	yaml := "http: {server: {tls: {enabled: true, cert_file: " + cert + ", key_file: " + key + "}}}\n"

	if hasWarning(loadWarnings(t, yaml), "key_file") {
		t.Fatal("an owner-only key must not be reported")
	}
	if err := os.Chmod(key, 0o644); err != nil {
		t.Fatal(err)
	}
	if !hasWarning(loadWarnings(t, yaml), "http.server.tls.key_file is readable by group or others (mode 0644)") {
		t.Fatal("a 0644 key must be reported")
	}
}

func TestDocsInProductionWarning(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir) // no overlay file: only the base config
	cfg, err := LoadFlags(writeConfig(t, "env: prd\n"+baseYAML+"prd_exceptions: {docs: true}\nhttp: {docs: {enabled: true}}\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	if !hasWarning(cfg.Warnings, "http.docs is enabled in prd") {
		t.Fatalf("docs in prd must be reported: %q", cfg.Warnings)
	}
	for name, yaml := range map[string]string{
		"dev":           "env: dev\n" + baseYAML + "http: {docs: {enabled: true}}\n",
		"prd, disabled": "env: prd\n" + baseYAML,
	} {
		cfg, err := LoadFlags(writeConfig(t, yaml), "")
		if err != nil {
			t.Fatal(name, err)
		}
		if hasWarning(cfg.Warnings, "http.docs is enabled") {
			t.Errorf("%s: unexpected warning %q", name, cfg.Warnings)
		}
	}
}

// A server with HTTPS or HTTP/3 whose cache or database is reached without TLS
// is reported; without TLS at the edge nothing is added.
func TestEdgeTLSWithPlaintextBackendsWarning(t *testing.T) {
	cert, key := testPair(t)
	tlsOn := "http: {server: {tls: {enabled: true, cert_file: " + cert + ", key_file: " + key + "}}}\n"
	h3Only := "http: {server: {http3: {enabled: true, same_keycert_tls: false, cert_file: " + cert + ", key_file: " + key + "}}}\n"
	redisPlain := "cache: {enabled: true, driver: redis, redis: {host: 127.0.0.1}}\n"
	redisTLS := "cache: {enabled: true, driver: redis, redis: {url: 'rediss://127.0.0.1:6379'}}\n"
	dbPlain := "database: {enabled: true, host: localhost, port: 5432, name: d, user: u, password: p}\n"
	dbTLS := "database: {enabled: true, host: localhost, port: 5432, name: d, user: u, password: p, ssl: {mode: verify-full}}\n"
	dbSocket := "database: {enabled: true, dsn: 'host=/var/run/postgresql user=u dbname=d'}\n"
	memory := "cache: {enabled: true, driver: memory}\n"

	cases := []struct {
		name, yaml string
		redis, db  bool
	}{
		{"https, redis and db plain", tlsOn + redisPlain + dbPlain, true, true},
		{"http3 only, redis and db plain", h3Only + redisPlain + dbPlain, true, true},
		{"https, everything encrypted", tlsOn + redisTLS + dbTLS, false, false},
		{"https, memory cache, db socket", tlsOn + memory + dbSocket, false, false},
		{"plain http edge", redisPlain + dbPlain, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := loadWarnings(t, c.yaml)
			if got := hasWarning(w, "HTTPS or HTTP/3 but cache.redis"); got != c.redis {
				t.Errorf("redis warning = %v, want %v: %q", got, c.redis, w)
			}
			if got := hasWarning(w, "HTTPS or HTTP/3 but the database"); got != c.db {
				t.Errorf("database warning = %v, want %v: %q", got, c.db, w)
			}
		})
	}
}

// In prd an insecure setting is a load error unless its prd_exceptions key is
// set; elsewhere it stays a warning.
func TestProductionRefusesInsecureSettings(t *testing.T) {
	t.Chdir(t.TempDir())
	const redis = "cache: {enabled: true, driver: redis, redis: {host: cache.internal, password: secret}}\n"
	const db = "database: {enabled: true, host: db.internal, port: 5432, name: app, user: app, password: secret}\n"
	cases := []struct {
		name, yaml, exception string
	}{
		{"docs", "http: {docs: {enabled: true}}\n", "docs"},
		{"redis", redis, "redis_plaintext"},
		{"database", db, "database_plaintext"},
	}
	for _, c := range cases {
		_, err := LoadFlags(writeConfig(t, "env: prd\n"+baseYAML+c.yaml), "")
		if err == nil || !strings.Contains(err.Error(), "prd_exceptions."+c.exception) {
			t.Errorf("%s: err = %v, want a refusal naming prd_exceptions.%s", c.name, err, c.exception)
		}
		cfg, err := LoadFlags(writeConfig(t, "env: prd\n"+baseYAML+"prd_exceptions: {"+c.exception+": true}\n"+c.yaml), "")
		if err != nil || len(cfg.Warnings) == 0 {
			t.Errorf("%s with the exception: err = %v, warnings = %q", c.name, err, cfg.Warnings)
		}
		if cfg, err := LoadFlags(writeConfig(t, "env: dev\n"+baseYAML+c.yaml), ""); err != nil || (c.name != "docs" && len(cfg.Warnings) == 0) {
			t.Errorf("%s in dev: err = %v, want a warning only", c.name, err)
		}
	}
}

func TestProductionRefusesLooseTLSKey(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits")
	}
	t.Chdir(t.TempDir())
	cert, key := testPair(t)
	if err := os.Chmod(key, 0o644); err != nil {
		t.Fatal(err)
	}
	yaml := "http: {server: {tls: {enabled: true, cert_file: " + cert + ", key_file: " + key + "}}}\n"
	if _, err := LoadFlags(writeConfig(t, "env: prd\n"+baseYAML+yaml), ""); err == nil || !strings.Contains(err.Error(), "prd_exceptions.tls_key_permissions") {
		t.Fatalf("err = %v", err)
	}
	if _, err := LoadFlags(writeConfig(t, "env: prd\n"+baseYAML+"prd_exceptions: {tls_key_permissions: true}\n"+yaml), ""); err != nil {
		t.Fatal(err)
	}
}
