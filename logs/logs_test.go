package logs

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/ams-soft/nforge/app"
	"github.com/ams-soft/nforge/internal/apptest"
	"github.com/ams-soft/nforge/module"
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
// stg/prd they publish build paths to the log backend for no benefit.
func TestSourceIsOnlyAddedInDebugEnvironments(t *testing.T) {
	cases := map[string]bool{"local": true, "dev": false, "stg": false, "prd": false}
	for env, wantSource := range cases {
		out := captureStdout(t, func() {
			var s Settings
			s.Output.Terminal.Enabled = true
			logger, shutdown, err := Setup(context.Background(), s, module.App{Name: "t", Env: env})
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

func TestModule(t *testing.T) {
	quiet := apptest.Start(t, "", Module())
	logger, ok := app.Value(quiet, module.Logger)
	if !ok || logger.Handler().Enabled(context.Background(), slog.LevelError) {
		t.Fatal("with no output the logger discards everything")
	}
	previous := slog.Default()
	out := captureStdout(t, func() {
		a := apptest.Start(t, "logging: {output: {terminal: {enabled: true}}}\n", Module())
		logger, _ := app.Value(a, module.Logger)
		logger.Error("hello")
		if slog.Default() != previous {
			t.Error("without app.WithProcessGlobals slog.Default stays untouched")
		}
	})
	if !strings.Contains(out, "hello") || !strings.Contains(out, "Logger setup completed") {
		t.Fatalf("output = %q", out)
	}
	if _, err := apptest.New(t, "logging: {output: {termnal: {enabled: true}}}\n", Module()); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("typo: err = %v", err)
	}
	s := Settings{Output: OutputSettings{Otel: OtelOutput{Endpoint: "https://u:s3cr3t@h:4317"}}}
	for _, text := range []string{s.String(), s.GoString(), fmt.Sprint(s)} {
		if strings.Contains(text, "s3cr3t") {
			t.Fatalf("leaked: %s", text)
		}
	}
}
