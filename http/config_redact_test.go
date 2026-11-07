package nhttp

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

// The private key paths never reach a log or a printout of the section.
func TestConfigNeverPrintsKeyPaths(t *testing.T) {
	var cfg Config
	cfg.Server.TLS.KeyFile = "/run/secrets/s3cr3t-tls.key"
	cfg.Server.HTTP3.KeyFile = "/run/secrets/s3cr3t-quic.key"
	var log strings.Builder
	slog.New(slog.NewJSONHandler(&log, nil)).Info("cfg", "http", cfg)
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, out := range []string{cfg.String(), cfg.GoString(), fmt.Sprintf("%v", cfg), fmt.Sprintf("%+v", cfg), log.String(), string(b)} {
		if strings.Contains(out, "s3cr3t") {
			t.Fatalf("leaked: %s", out)
		}
	}
	if cfg.Server.TLS.KeyFile == redactedValue {
		t.Fatal("Redacted must not change the original")
	}
}
