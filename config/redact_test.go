package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/ams-soft/nforge/jose"
	"gopkg.in/yaml.v3"
)

// secretConfig plants a recognizable secret in every field that can hold one.
func secretConfig() (Config, []string) {
	var cfg Config
	cfg.App.Name = "x"
	cfg.Database.Password = "S3CRET-DBPASS"
	cfg.Database.DSN = "postgres://u:S3CRET-DSNPASS@h/db?sslpassword=S3CRET-SSLPASS"
	cfg.Cache.Redis.Password = "S3CRET-REDISPASS"
	cfg.Cache.Redis.URL = "redis://:S3CRET-REDISURL@h:6379"
	cfg.Cache.Redis.TLS.CAFile = "/etc/S3CRET-ca/ca.pem"
	cfg.Logging.Output.Otel.Endpoint = "https://user:S3CRET-OTEL@collector:4317"
	cfg.Tracing.Otel.Endpoint = "https://user:S3CRET-TRACE@collector:4317"
	cfg.HTTP.Server.TLS.KeyFile = "/etc/S3CRET-tlskey/key.pem"
	cfg.HTTP.Server.HTTP3.KeyFile = "/etc/S3CRET-h3key/key.pem"
	cfg.JOSE.Keys = []jose.KeyConfig{
		{KID: "k1", Secret: "S3CRET-JOSESECRET-32-bytes-long-xxxxx"},
		{KID: "k2", Source: jose.KeySourceConfig{Format: "pem", File: "/etc/S3CRET-josefile/k.pem"}},
	}
	secrets := []string{
		"S3CRET-DBPASS", "S3CRET-DSNPASS", "S3CRET-SSLPASS", "S3CRET-REDISPASS", "S3CRET-REDISURL", "S3CRET-ca",
		"S3CRET-OTEL", "S3CRET-TRACE", "S3CRET-tlskey", "S3CRET-h3key", "S3CRET-JOSESECRET", "S3CRET-josefile",
	}
	return cfg, secrets
}

// TestConfigNeverPrintsSecrets prints a fully populated Config through every
// path a developer is likely to use and fails if any planted secret shows up.
func TestConfigNeverPrintsSecrets(t *testing.T) {
	cfg, secrets := secretConfig()

	var jsonLog, textLog bytes.Buffer
	slog.New(slog.NewJSONHandler(&jsonLog, nil)).Info("cfg", "config", cfg)
	slog.New(slog.NewTextHandler(&textLog, nil)).Info("cfg", "config", cfg)
	marshaled, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	yamled, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	outputs := map[string]string{
		"%v":        fmt.Sprintf("%v", cfg),
		"%+v":       fmt.Sprintf("%+v", cfg),
		"%#v":       fmt.Sprintf("%#v", cfg),
		"%v(ptr)":   fmt.Sprintf("%v", &cfg),
		"String":    cfg.String(),
		"slog-json": jsonLog.String(),
		"slog-text": textLog.String(),
		"json":      string(marshaled),
		"yaml":      string(yamled),
		"database":  fmt.Sprintf("%+v", cfg.Database),
	}
	for name, out := range outputs {
		for _, s := range secrets {
			if strings.Contains(out, s) {
				t.Errorf("%s leaks %q", name, s)
			}
		}
	}
	if !strings.Contains(jsonLog.String(), redactedValue) {
		t.Errorf("slog JSON output shows no redaction marker: %s", jsonLog.String())
	}
}

func TestRedactedDoesNotMutateTheOriginal(t *testing.T) {
	cfg, _ := secretConfig()
	_ = cfg.Redacted()
	if cfg.Database.Password != "S3CRET-DBPASS" || cfg.JOSE.Keys[0].Secret == redactedValue || cfg.JOSE.Keys[1].Source.File == redactedValue {
		t.Fatalf("Redacted changed the original: %+v", cfg.JOSE.Keys)
	}
}

func TestRedactEndpoint(t *testing.T) {
	cases := map[string]string{
		"localhost:4317":                  "localhost:4317",
		"":                                "",
		"http://collector:4318":           "http://collector:4318",
		"https://user:tok@collector:4317": "https://%5BREDACTED%5D@collector:4317",
		"https://collector:4317?api_key=S3CRET-QUERY":    "https://collector:4317",
		"https://u:p@collector/p?token=S3CRET-QUERY#frg": "https://%5BREDACTED%5D@collector/p#frg",
	}
	for in, want := range cases {
		if got := redactEndpoint(in); got != want {
			t.Errorf("redactEndpoint(%q) = %q, want %q", in, got, want)
		}
	}
}
