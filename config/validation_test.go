package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// G17: app.name flows into logs, OTel, AMQP and cache prefixes.
func TestAppNameIsValidated(t *testing.T) {
	for name, tc := range map[string]struct {
		yaml, want string
	}{
		"control":  {"app: {name: \"a\\nb\", version: 1}\n", "control characters"},
		"ansi":     {"app: {name: \"a\\u001b[31m\", version: 1}\n", "control characters"},
		"too long": {"app: {name: \"" + strings.Repeat("n", 129) + "\", version: 1}\n", "at most 128"},
	} {
		dir := t.TempDir()
		p := filepath.Join(dir, "config.yaml")
		if err := os.WriteFile(p, []byte(tc.yaml), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadFlags(p, ""); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
}

// G18: the YAML error for a ${VAR} inside a flow mapping says what to do.
func TestFlowMappingPlaceholderErrorHasAHint(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte("app: {name: x, version: 1}\nhttp: {server: {port: ${P:-8083}}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadFlags(p, "")
	if err == nil || !strings.Contains(err.Error(), "block style") {
		t.Fatalf("err = %v, want a hint about block style", err)
	}
}
