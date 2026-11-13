package scheduler_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
	_ "time/tzdata"

	"github.com/ams-soft/nforge/app"
	"github.com/ams-soft/nforge/internal/apptest"
	"github.com/ams-soft/nforge/logs"
	"github.com/ams-soft/nforge/metrics"
	"github.com/ams-soft/nforge/module"
	"github.com/ams-soft/nforge/scheduler"
)

// domain is an application module that registers one job in Init.
func domain(add func(*scheduler.Scheduler) error) module.Descriptor {
	return module.New("stock", module.Definition{
		Requires: []module.Handle{scheduler.Capability},
		Init: func(_ context.Context, sc *module.Scope) (module.Hooks, error) {
			return module.Hooks{}, add(module.Require(sc, scheduler.Capability))
		},
	})
}

func noop(context.Context) error { return nil }

func TestConfigErrorsFailAppNew(t *testing.T) {
	for name, yaml := range map[string]string{
		"distributed": "scheduler: {mode: distributed}\n",
		"bad mode":    "scheduler: {mode: cluster}\n",
		"bad zone":    "scheduler: {timezone: Mars/Olympus}\n",
		"negative":    "scheduler: {max_concurrency: -1}\n",
		"unknown key": "scheduler: {retries: 3}\n",
	} {
		_, err := apptest.New(t, yaml, scheduler.Module())
		if err == nil {
			t.Errorf("%s: app.New succeeded", name)
			continue
		}
		if name == "distributed" && !errors.Is(err, scheduler.ErrUnsupportedMode) {
			t.Errorf("distributed: %v, want ErrUnsupportedMode (no fallback to local)", err)
		}
	}
}

func TestDefaultsNeedNoSection(t *testing.T) {
	a := apptest.Start(t, "", logs.Module(), scheduler.Module())
	s, ok := app.Value(a, scheduler.Capability)
	if !ok || s.Location().String() != "UTC" {
		t.Fatalf("scheduler = %v, %v", s, ok)
	}
}

func TestDisabledSchedulerFailsItsDependents(t *testing.T) {
	_, err := apptest.New(t, "scheduler: {enabled: false}\n", scheduler.Module(), domain(func(*scheduler.Scheduler) error { return nil }))
	if !errors.Is(err, module.ErrDisabledDependency) {
		t.Fatalf("err = %v, want ErrDisabledDependency", err)
	}
	_, err = apptest.New(t, "", domain(func(*scheduler.Scheduler) error { return nil }))
	if !errors.Is(err, module.ErrMissingDependency) {
		t.Fatalf("unregistered: %v, want ErrMissingDependency", err)
	}
}

func TestSectionWithoutModuleIsAnError(t *testing.T) {
	if _, err := apptest.New(t, "scheduler: {timezone: UTC}\n", logs.Module()); err == nil {
		t.Fatal("a scheduler section without the module must fail")
	}
}

func TestSchedulerOnlyApplicationStartsReadyAndStops(t *testing.T) {
	var added atomic.Bool
	a := apptest.Start(t, "scheduler: {timezone: America/Sao_Paulo}\n", logs.Module(), scheduler.Module(),
		domain(func(s *scheduler.Scheduler) error { added.Store(true); return s.AddJob("0 7 * * *", noop) }))
	if !added.Load() {
		t.Fatal("Init did not run")
	}
	rep := a.Status(context.Background())
	if !rep.Ready {
		t.Fatalf("not ready: %+v", rep)
	}
	s, _ := app.Value(a, scheduler.Capability)
	if s.Location().String() != "America/Sao_Paulo" {
		t.Fatalf("zone = %v", s.Location())
	}
	// Registration after Start is refused.
	if err := s.AddJob("0 8 * * *", noop); !errors.Is(err, scheduler.ErrStarted) {
		t.Fatalf("AddJob after start = %v", err)
	}
	if err := a.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := a.Shutdown(context.Background()); err != nil { // idempotent
		t.Fatal(err)
	}
	if err := s.AddJob("0 8 * * *", noop); !errors.Is(err, scheduler.ErrClosed) {
		t.Fatalf("AddJob after stop = %v", err)
	}
}

func TestInvalidExpressionInInitFailsStartup(t *testing.T) {
	a, err := apptest.New(t, "", scheduler.Module(), domain(func(s *scheduler.Scheduler) error { return s.AddJob("0 7 * *", noop) }))
	if err != nil {
		t.Fatal(err)
	}
	err = a.Start(context.Background())
	if !errors.Is(err, scheduler.ErrInvalidSchedule) {
		t.Fatalf("Start = %v, want ErrInvalidSchedule (a startup failure, not a silent one later)", err)
	}
	var le *module.LifecycleError
	if !errors.As(err, &le) || le.Module != "stock" {
		t.Fatalf("error = %#v", err)
	}
}

// A scheduler-only application is kept alive by app.Run, with no HTTP and no
// broker, until it is told to stop.
func TestRunKeepsASchedulerOnlyApplicationAlive(t *testing.T) {
	a, err := apptest.New(t, "", logs.Module(), scheduler.Module(), domain(func(s *scheduler.Scheduler) error { return s.AddJob("0 7 * * *", noop) }))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	select {
	case err := <-done:
		t.Fatalf("Run returned early: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if !a.Status(context.Background()).Ready {
		t.Fatal("not ready while running")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after the context ended")
	}
}

func TestMetricsAreRegisteredWhenPresent(t *testing.T) {
	a := apptest.Start(t, "", metrics.Module(), scheduler.Module(), domain(func(s *scheduler.Scheduler) error { return s.AddJob("0 7 * * *", noop) }))
	reg, ok := app.Value(a, metrics.Capability)
	if !ok {
		t.Fatal("no registry")
	}
	var out bytes.Buffer
	if err := reg.Render(&out); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte("nforge_scheduler_runs_total")) {
		t.Fatalf("scheduler metrics missing:\n%s", out.String())
	}
}

func TestBuildsWithoutLoggerOrMetrics(t *testing.T) {
	a := apptest.Start(t, "", scheduler.Module())
	if !a.Status(context.Background()).Ready {
		t.Fatal("not ready")
	}
	_ = slog.Default()
}
