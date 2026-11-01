package logs

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/ams-soft/nforge/config"
)

// captureStdout returns what logger output written during fn reached os.Stdout.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = old }()
	fn()
	_ = w.Close()
	out, _ := io.ReadAll(r)
	return string(out)
}

// The source file and line go in every record only while developing: in
// stg/prd they publish build paths to the log backend for no benefit (G-18).
func TestSourceIsOnlyAddedInDebugEnvironments(t *testing.T) {
	cases := map[string]bool{"local": true, "dev": false, "stg": false, "prd": false}
	for env, wantSource := range cases {
		out := captureStdout(t, func() {
			var cfg config.Config
			cfg.Env, cfg.App.Name = env, "t"
			cfg.Logging.Output.Terminal.Enabled = true
			logger, shutdown, err := Setup(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = shutdown(context.Background()) }()
			logger.LogAttrs(context.Background(), slog.LevelError, "boom")
		})
		if !strings.Contains(out, "boom") {
			t.Fatalf("%s: nothing logged: %q", env, out)
		}
		if has := strings.Contains(out, "logs_test.go"); has != wantSource {
			t.Errorf("%s: source in output = %v, want %v: %q", env, has, wantSource, out)
		}
	}
}
