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

// A TLS private key that others can read is reported, not refused: many
// orchestrators mount secrets 0644 (G-09).
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
	cfg, err := LoadFlags(writeConfig(t, "env: prd\n"+baseYAML+"http: {docs: {enabled: true}}\n"), "")
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
