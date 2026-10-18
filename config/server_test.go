package config

import (
	"strings"
	"testing"
	"time"
)

func TestServerTimeoutDefaults(t *testing.T) {
	cfg, err := loadTenantConfig(t, "http: {server: {port: 8080}}", "")
	if err != nil {
		t.Fatal(err)
	}
	s := cfg.HTTP.Server
	if s.ReadHeaderTimeout != 10*time.Second || s.ReadTimeout != 30*time.Second ||
		s.WriteTimeout != 30*time.Second || s.IdleTimeout != 120*time.Second {
		t.Fatalf("http.server timeouts = %+v", s)
	}
}

func TestServerTimeoutYAMLAndEnvFile(t *testing.T) {
	yamlText := "http: {server: {port: 8080, read_header_timeout: 2s, write_timeout: 5m}}\n"
	cfg, err := loadTenantConfig(t, yamlText, "HTTP_SERVER_READ_TIMEOUT=1m\nHTTP_SERVER_IDLE_TIMEOUT=30s\n")
	if err != nil {
		t.Fatal(err)
	}
	s := cfg.HTTP.Server
	if s.ReadHeaderTimeout != 2*time.Second || s.ReadTimeout != time.Minute ||
		s.WriteTimeout != 5*time.Minute || s.IdleTimeout != 30*time.Second {
		t.Fatalf("http.server timeouts = %+v", s)
	}
}

func TestServerTimeoutValidation(t *testing.T) {
	cases := map[string]string{
		"read_header_timeout: -1s":                  "read_header_timeout must be positive",
		"read_timeout: -1s":                         "read_timeout must be positive",
		"write_timeout: -1s":                        "write_timeout must be positive",
		"idle_timeout: -1s":                         "idle_timeout must be positive",
		"read_header_timeout: 1m":                   "must not exceed http.server.read_timeout",
		"read_header_timeout: 5s, read_timeout: 1s": "must not exceed http.server.read_timeout",
	}
	for fields, want := range cases {
		_, err := loadTenantConfig(t, "http: {server: {port: 8080, "+fields+"}}\n", "")
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", fields, err, want)
		}
	}
}

func TestApplyServerDefaultsKeepsSetValues(t *testing.T) {
	s := ServerConfig{WriteTimeout: time.Hour}
	ApplyServerDefaults(&s)
	if s.WriteTimeout != time.Hour || s.ReadHeaderTimeout != defaultReadHeaderTimeout ||
		s.ReadTimeout != defaultReadTimeout || s.IdleTimeout != defaultIdleTimeout {
		t.Fatalf("ApplyServerDefaults = %+v", s)
	}
}
