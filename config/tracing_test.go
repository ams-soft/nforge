package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsTracingEnabled(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want bool
	}{
		{
			name: "otel on, tracing omitted defaults to true",
			yaml: "logging:\n  output:\n    otel:\n      enabled: true\n",
			want: true,
		},
		{
			name: "otel on, tracing explicitly true",
			yaml: "logging:\n  output:\n    otel:\n      enabled: true\ntracing:\n  otel:\n    enabled: true\n",
			want: true,
		},
		{
			name: "otel on, tracing explicitly false",
			yaml: "logging:\n  output:\n    otel:\n      enabled: true\ntracing:\n  otel:\n    enabled: false\n",
			want: false,
		},
		{
			name: "otel off ignores tracing true",
			yaml: "logging:\n  output:\n    otel:\n      enabled: false\ntracing:\n  otel:\n    enabled: true\n",
			want: false,
		},
		{
			name: "otel omitted",
			yaml: "",
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			base := "app:\n  name: test\n  version: 0.0.1\nhttp:\n  server:\n    port: 8080\n"
			if err := os.WriteFile(path, []byte(base+tc.yaml), 0o600); err != nil {
				t.Fatal(err)
			}

			cfg, err := LoadFlags(path, "")
			if err != nil {
				t.Fatalf("LoadFlags: %v", err)
			}
			if got := cfg.IsTracingEnabled(); got != tc.want {
				t.Errorf("IsTracingEnabled() = %v, want %v", got, tc.want)
			}
		})
	}
}
