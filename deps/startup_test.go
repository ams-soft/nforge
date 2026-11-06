package deps

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ams-soft/nforge/cache"
	"github.com/ams-soft/nforge/config"
	"github.com/ams-soft/nforge/jose"
	"github.com/ams-soft/nforge/module"
)

func badDatabaseConfig() config.Config {
	var cfg config.Config
	cfg.Env, cfg.App.Name, cfg.App.Version = "local", "t", "1"
	cfg.Database.Enabled = true
	cfg.Database.DSN = "postgres://u:p@127.0.0.1:1/db?connect_timeout=1" // nothing listens on port 1
	return cfg
}

// NewDepsE returns the startup error instead of exiting.
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

// On failure the modules started before it are shut down in reverse order and
// the failure is logged first, so an OTel batch is flushed and nothing leaks.
func TestFailedStartLogsAndRollsBackEarlierModules(t *testing.T) {
	var buf bytes.Buffer
	var closed []string
	closer := func(name string, err error) func(context.Context) error {
		return func(context.Context) error { closed = append(closed, name); return err }
	}
	cause := errors.New("unable to setup worker: boom")
	first := module.NewCapability[int]("first")
	logged := module.NewCapability[*slog.Logger]("test-log")
	mods := []Module{
		{ID: "log", Provides: []module.CapabilityID{logged.ID()}, Init: func(_ context.Context, _ config.Config, s *module.Scope) (module.Hooks, error) {
			module.Provide(s, logged, slog.New(slog.NewTextHandler(&buf, nil)))
			return module.Hooks{Shutdown: closer("log", nil)}, nil
		}},
		{ID: "first", Provides: []module.CapabilityID{first.ID()}, Init: func(_ context.Context, _ config.Config, s *module.Scope) (module.Hooks, error) {
			module.Provide(s, first, 1)
			return module.Hooks{Shutdown: closer("first", errors.New("close failed"))}, nil
		}},
		{ID: "worker", Requires: caps(first.ID()), Init: func(context.Context, config.Config, *module.Scope) (module.Hooks, error) {
			return module.Hooks{}, cause
		}},
	}
	d, err := Bootstrap(context.Background(), minimalConfig(), WithModules(mods...))
	if !errors.Is(err, cause) || !strings.Contains(err.Error(), "close failed") {
		t.Fatalf("the cause and the rollback errors must be joined: %v", err)
	}
	if d != nil {
		t.Fatal("a failed bootstrap returns no Deps")
	}
	if want := []string{"first", "log"}; !slices.Equal(closed, want) {
		t.Fatalf("rollback order = %v, want %v", closed, want)
	}
}

// The failing module can be told from the error, whose text stays the cause's.
func TestStartFailureNamesTheModule(t *testing.T) {
	boom := errors.New("boom")
	_, err := Bootstrap(context.Background(), minimalConfig(), WithModules(Module{ID: "bad", Init: func(context.Context, config.Config, *module.Scope) (module.Hooks, error) {
		return module.Hooks{}, boom
	}}))
	var init *module.InitError
	if !errors.Is(err, boom) || !errors.As(err, &init) || init.Module != "bad" {
		t.Fatalf("err = %v", err)
	}
}

// A module that needs a disabled one fails before anything starts, and the
// provider stays off: no implicit activation.
func TestDisabledDependencyFailsBeforeAnythingStarts(t *testing.T) {
	started := false
	consumer := Module{
		ID:       "consumer",
		Requires: caps(CapDatabase.ID()),
		Init: func(context.Context, config.Config, *module.Scope) (module.Hooks, error) {
			started = true
			return module.Hooks{}, nil
		},
	}
	_, err := Bootstrap(context.Background(), minimalConfig(), WithModules(consumer))
	if !errors.Is(err, module.ErrDisabledDependency) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), `"consumer"`) || !strings.Contains(err.Error(), "database") {
		t.Fatalf("the error must name both sides: %v", err)
	}
	if started {
		t.Fatal("no module may start when the graph is invalid")
	}
}

// http.auth.default_profile needs jose; config.Load refuses it, and a Config
// built by hand gets the same answer from the graph.
func TestDefaultProfileRequiresJOSE(t *testing.T) {
	cfg := minimalConfig()
	cfg.HTTP.Auth.DefaultProfile = "access"
	if _, err := NewDepsE(cfg); !errors.Is(err, module.ErrDisabledDependency) {
		t.Fatalf("err = %v", err)
	}
}

// A consumer shares the resource of its provider: one pool, closed once, by the
// owner.
func TestExtraModuleReceivesTheProvidedResource(t *testing.T) {
	cfg := minimalConfig()
	cfg.Cache = cache.Config{Enabled: true, Driver: cache.DriverMemory}
	cache.ApplyDefaults(&cfg.Cache)
	var got *cache.Cache
	out := module.NewCapability[string]("report")
	d, err := Bootstrap(context.Background(), cfg, WithModules(Module{
		ID:       "consumer",
		Provides: []module.CapabilityID{out.ID()},
		Requires: caps(CapCache.ID()),
		Init: func(_ context.Context, _ config.Config, s *module.Scope) (module.Hooks, error) {
			got = module.Require(s, CapCache)
			module.Provide(s, out, "done")
			return module.Hooks{}, nil
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got != d.Cache {
		t.Fatal("the consumer must receive the instance in Deps.Cache")
	}
	if v, ok := Value(d, out); !ok || v != "done" {
		t.Fatalf("Value = %q %v", v, ok)
	}
	if err := d.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// The start sequence and the shutdown of v0.2.0 are preserved.
func TestBuiltinOrder(t *testing.T) {
	cfg := minimalConfig()
	cfg.Logging.Output.Otel.Enabled = true
	cfg.Database.Enabled = true
	cfg.JOSE.Enabled = true
	cfg.Cache.Enabled = true
	plan, err := module.Resolve(cfg, builtinModules(false))
	if err != nil {
		t.Fatal(err)
	}
	want := []module.ID{"logger", "tracing", "startup-report", "codec", "database", "jose", "cache", "http"}
	if got := plan.Snapshot().Order; !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

// A Bootstrap ctx ends the start, not the application.
func TestBootstrapContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Bootstrap(ctx, minimalConfig()); !errors.Is(err, context.Canceled) {
		t.Fatalf("a canceled ctx must abort the start: %v", err)
	}

	ctx, cancel = context.WithCancel(context.Background())
	var moduleCtx context.Context
	d, err := Bootstrap(ctx, minimalConfig(), WithModules(Module{ID: "bg", Init: func(c context.Context, _ config.Config, _ *module.Scope) (module.Hooks, error) {
		moduleCtx = c
		return module.Hooks{}, nil
	}}))
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if moduleCtx.Err() != nil {
		t.Fatal("canceling the Bootstrap ctx after it returned must not stop the modules")
	}
	if err := d.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if moduleCtx.Err() == nil {
		t.Fatal("Shutdown must end the context given to Init")
	}
}

// Status separates disabled from unavailable, and Shutdown is reflected.
func TestModuleStatus(t *testing.T) {
	d, err := NewDepsE(minimalConfig())
	if err != nil {
		t.Fatal(err)
	}
	byID := func() map[module.ID]module.Status {
		out := map[module.ID]module.Status{}
		for _, s := range d.ModuleStatus(context.Background()) {
			out[s.ID] = s
		}
		return out
	}
	got := byID()
	if s := got["database"]; s.Availability != module.Disabled || s.Initialization != module.StateDisabled {
		t.Fatalf("database = %+v", s)
	}
	if s := got["codec"]; s.Availability != module.Ready || s.Initialization != module.StateStarted {
		t.Fatalf("codec = %+v", s)
	}
	if err := d.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s := byID()["codec"]; s.Availability != module.NotReady || s.Initialization != module.StateStopped {
		t.Fatalf("after shutdown: %+v", s)
	}
	var empty Deps
	if _, ok := empty.Graph(); ok || empty.ModuleStatus(context.Background()) != nil {
		t.Fatal("a Deps not built by Bootstrap has no graph")
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
// stay quiet.
func TestShutdownIsSafeToCallConcurrently(t *testing.T) {
	var runs atomic.Int32
	var d Deps
	for range 3 {
		d.AddShutdown(func(context.Context) error { runs.Add(1); return nil })
	}
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() { _ = d.Shutdown(context.Background()) })
	}
	wg.Wait()
	if got := runs.Load(); got != 3 {
		t.Fatalf("closers ran %d times, want exactly 3", got)
	}
	if err := d.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}
