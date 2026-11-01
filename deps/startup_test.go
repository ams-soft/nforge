package deps

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ams-soft/nforge/config"
	"github.com/ams-soft/nforge/jose"
)

func badDatabaseConfig() config.Config {
	var cfg config.Config
	cfg.Env, cfg.App.Name, cfg.App.Version = "local", "t", "1"
	cfg.Database.Enabled = true
	cfg.Database.DSN = "postgres://u:p@127.0.0.1:1/db?connect_timeout=1" // nothing listens on port 1
	return cfg
}

// NewDepsE returns the startup error instead of exiting (G-03).
func TestNewDepsEReturnsTheStartupError(t *testing.T) {
	_, err := NewDepsE(badDatabaseConfig())
	if err == nil || !strings.Contains(err.Error(), "unable to setup database") {
		t.Fatalf("want a database setup error, got %v", err)
	}
	if strings.Contains(err.Error(), "p@127.0.0.1") {
		t.Errorf("the error must not echo the DSN password: %v", err)
	}

	var cfg config.Config
	cfg.Env, cfg.App.Name, cfg.App.Version = "local", "t", "1"
	cfg.JOSE = jose.Config{Enabled: true, Keys: []jose.KeyConfig{{KID: "k", Algorithm: jose.EdDSA,
		Source: jose.KeySourceConfig{Format: "pem", File: "/nonexistent/key.pem"}}}}
	if _, err := NewDepsE(cfg); err == nil || !strings.Contains(err.Error(), "unable to setup jose") {
		t.Fatalf("want a jose setup error, got %v", err)
	}
}

// On failure the modules started before it are shut down and the failure is
// logged, so an OTel batch is flushed and nothing leaks.
func TestAbortLogsAndShutsDownEarlierSteps(t *testing.T) {
	var buf bytes.Buffer
	closed := 0
	d := Deps{
		Context: context.Background(),
		Logger:  *slog.New(slog.NewTextHandler(&buf, nil)),
		Aux: &Aux{ShutdownFuncs: []func(context.Context) error{
			func(context.Context) error { closed++; return nil },
			func(context.Context) error { closed++; return errors.New("close failed") },
		}},
	}
	cause := errors.New("unable to setup cache: boom")
	err := d.abort(cause)
	if !errors.Is(err, cause) || !strings.Contains(err.Error(), "close failed") {
		t.Fatalf("abort must join the cause and the shutdown errors: %v", err)
	}
	if closed != 2 {
		t.Fatalf("closed %d of 2 started modules", closed)
	}
	if out := buf.String(); !strings.Contains(out, "level=ERROR") || !strings.Contains(out, "[Fatal ERROR]") || !strings.Contains(out, "boom") {
		t.Fatalf("the failure was not logged: %q", out)
	}
}

// NewDeps with logging disabled (the default) used to exit 1 and print
// nothing; the reason must reach stderr. It exits, so it runs in a subprocess.
func TestNewDepsPrintsTheFatalReasonToStderr(t *testing.T) {
	if os.Getenv("NFORGE_TEST_NEWDEPS_FATAL") == "1" {
		NewDeps(badDatabaseConfig())
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestNewDepsPrintsTheFatalReasonToStderr$")
	cmd.Env = append(os.Environ(), "NFORGE_TEST_NEWDEPS_FATAL=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("want exit status 1, got %v", err)
	}
	if out := stderr.String(); !strings.Contains(out, "[Fatal ERROR]") || !strings.Contains(out, "unable to setup database") {
		t.Fatalf("stderr = %q", out)
	}
}

// Shutdown is reachable from StartContext and from an application's own
// signal handler: a closer must never run twice, and the race detector must
// stay quiet (G-40).
func TestShutdownIsSafeToCallConcurrently(t *testing.T) {
	var runs atomic.Int32
	d := Deps{Aux: &Aux{ShutdownFuncs: []func(context.Context) error{
		func(context.Context) error { runs.Add(1); return nil },
		func(context.Context) error { runs.Add(1); return nil },
		func(context.Context) error { runs.Add(1); return nil },
	}}}
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() { _ = d.Shutdown(context.Background()) })
	}
	wg.Wait()
	if got := runs.Load(); got != 3 {
		t.Fatalf("closers ran %d times, want exactly 3", got)
	}
	d.ClearAux()
	if err := d.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}
