package app

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ams-soft/nforge/config"
	"github.com/ams-soft/nforge/module"
)

func load(t *testing.T, yaml string) *config.Document {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("app: {name: test, version: '1'}\n"+yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := config.LoadFlags(path, "")
	if err != nil {
		t.Fatal(err)
	}
	return d
}

type events struct {
	mu   sync.Mutex
	list []string
}

func (e *events) add(s string) { e.mu.Lock(); e.list = append(e.list, s); e.mu.Unlock() }
func (e *events) get() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.list)
}

var (
	storeCap = module.NewCapability[*store]("store")
	queueCap = module.NewCapability[*queue]("queue")
)

// store is a synthetic resource with a typed configuration section.
type store struct {
	cfg    storeConfig
	closed bool
	mu     sync.Mutex
	down   bool
}

type storeConfig struct {
	Enabled *bool  `yaml:"enabled"`
	DSN     string `yaml:"dsn"`
	Pool    int    `yaml:"pool"`
}

func (s *store) use() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("store closed while in use")
	}
	return nil
}

func storeModule(ev *events) module.Descriptor {
	return module.Descriptor{ID: "store", Section: "store", Configure: func(src module.Source) (module.Definition, error) {
		cfg := storeConfig{Pool: 4}
		if err := src.Decode(&cfg); err != nil {
			return module.Definition{}, err
		}
		if cfg.Pool <= 0 {
			return module.Definition{}, errors.New("store.pool must be positive")
		}
		if strings.Contains(cfg.DSN, "sslmode=disable") {
			src.Warn("database_plaintext", "store.dsn disables TLS")
		}
		s := &store{cfg: cfg}
		return module.Definition{
			Disabled: cfg.Enabled != nil && !*cfg.Enabled,
			Provides: []module.Handle{storeCap}, Config: cfg,
			Init: func(_ context.Context, sc *module.Scope) (module.Hooks, error) {
				ev.add("init store")
				module.Provide(sc, storeCap, s)
				return module.Hooks{
					Ready: func(context.Context) error {
						s.mu.Lock()
						defer s.mu.Unlock()
						if s.down {
							return errors.New("down")
						}
						return nil
					},
					Stop: func(context.Context) error {
						ev.add("stop store")
						s.mu.Lock()
						s.closed = true
						s.mu.Unlock()
						return nil
					},
				}, nil
			},
		}, nil
	}}
}

// queue is a synthetic ingress: a consumer loop that runs handlers until Stop.
type queue struct {
	mu       sync.Mutex
	handlers []func(context.Context) error
	jobs     chan struct{}
	failed   func(error)
}

func (q *queue) Handle(h func(context.Context) error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.handlers = append(q.handlers, h)
}

func queueModule(ev *events) module.Descriptor {
	return module.New("worker", module.Definition{
		Provides: []module.Handle{queueCap}, Ingress: true,
		Init: func(_ context.Context, sc *module.Scope) (module.Hooks, error) {
			q := &queue{jobs: make(chan struct{}, 8), failed: sc.ReportFailure}
			module.Provide(sc, queueCap, q)
			ctx, cancel := context.WithCancel(sc.Context())
			var wg sync.WaitGroup
			return module.Hooks{
				Start: func(context.Context) error {
					q.mu.Lock()
					n := len(q.handlers)
					q.mu.Unlock()
					ev.add("start worker with " + string(rune('0'+n)) + " handlers")
					wg.Go(func() {
						for {
							select {
							case <-ctx.Done():
								return
							case <-q.jobs:
								for _, h := range q.handlers {
									if err := h(ctx); err != nil {
										q.failed(err)
									}
								}
							}
						}
					})
					return nil
				},
				Stop: func(context.Context) error { cancel(); wg.Wait(); ev.add("stop worker"); return nil },
			}, nil
		},
	})
}

// domain registers a job handler that uses the store.
func domainModule(ev *events, used chan<- error) module.Descriptor {
	return module.New("orders", module.Definition{
		Requires: []module.Handle{storeCap, queueCap},
		Init: func(_ context.Context, sc *module.Scope) (module.Hooks, error) {
			s := module.Require(sc, storeCap)
			module.Require(sc, queueCap).Handle(func(context.Context) error {
				err := s.use()
				used <- err
				return err
			})
			ev.add("init orders")
			return module.Hooks{Stop: func(context.Context) error { ev.add("stop orders"); return s.use() }}, nil
		},
	})
}

// A worker-only application: no HTTP module, Run is driven by ctx and the
// consumer drains before the store closes.
func TestWorkerOnlyApplication(t *testing.T) {
	var ev events
	used := make(chan error, 4)
	a, err := New(load(t, "store: {dsn: 'postgres://h/db'}\n"), Modules(storeModule(&ev), queueModule(&ev), domainModule(&ev, used)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	waitReady(t, a)
	q, _ := Value(a, queueCap)
	q.jobs <- struct{}{}
	if err := <-used; err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}
	want := []string{"init store", "init orders", "start worker with 1 handlers", "stop worker", "stop orders", "stop store"}
	if got := ev.get(); !slices.Equal(got, want) {
		t.Fatalf("events = %v", got)
	}
	if r := a.Status(context.Background()); r.Phase != module.PhaseStopped || r.Ready {
		t.Fatalf("status after run = %+v", r)
	}
}

func waitReady(t *testing.T, a *Application) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !a.Status(context.Background()).Ready {
		if time.Now().After(deadline) {
			t.Fatalf("not ready: %+v", a.Status(context.Background()))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A module that fails at runtime ends Run with its error, after a full shutdown.
func TestRuntimeFailureShutsDown(t *testing.T) {
	var ev events
	used := make(chan error, 4)
	a, err := New(load(t, ""), Modules(storeModule(&ev), queueModule(&ev), domainModule(&ev, used)), WithProbeInterval(time.Nanosecond, time.Second))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- a.Run(context.Background()) }()
	waitReady(t, a)
	s, _ := Value(a, storeCap)
	s.mu.Lock()
	s.closed = true // the handler will fail
	s.mu.Unlock()
	q, _ := Value(a, queueCap)
	q.jobs <- struct{}{}
	if err := <-used; err == nil {
		t.Fatal("the handler must fail on a closed store")
	}
	select {
	case err := <-done:
		var le *module.LifecycleError
		if !errors.As(err, &le) || le.Module != "worker" || le.Phase != module.PhaseRun {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not end on a runtime failure")
	}
	if got := ev.get(); !slices.Contains(got, "stop store") {
		t.Fatalf("events = %v", got)
	}
}

func TestReadinessTransitions(t *testing.T) {
	var ev events
	a, err := New(load(t, ""), Modules(storeModule(&ev)), WithProbeInterval(time.Nanosecond, time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if r := a.Status(context.Background()); r.Ready || r.Phase != module.PhaseValidated {
		t.Fatalf("before start: %+v", r)
	}
	if err := a.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Shutdown(context.Background()) }()
	s, _ := Value(a, storeCap)
	for _, down := range []bool{false, true, false} {
		s.mu.Lock()
		s.down = down
		s.mu.Unlock()
		time.Sleep(time.Millisecond)
		if r := a.Status(context.Background()); r.Ready == down {
			t.Fatalf("down=%v: %+v", down, r)
		}
	}
}

// Every configuration problem is reported at New, together, before any Init.
func TestNewAggregatesConfigurationErrors(t *testing.T) {
	var ev events
	_, err := New(load(t, "store: {pool: -1}\nstorr: {}\n"), Modules(storeModule(&ev), domainModule(&ev, nil)))
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{"store.pool must be positive", `section "storr" belongs to no registered module`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in %v", want, err)
		}
	}
	if len(ev.get()) != 0 {
		t.Fatal("nothing may be initialized by New")
	}
	// with the configuration fixed, the missing provider is the error
	_, err = New(load(t, ""), Modules(storeModule(&ev), domainModule(&ev, nil)))
	if !errors.Is(err, module.ErrMissingDependency) {
		t.Fatalf("err = %v", err)
	}
	_, err = New(load(t, "store: {enabled: false}\n"), Modules(storeModule(&ev), queueModule(&ev), domainModule(&ev, nil)))
	if !errors.Is(err, module.ErrDisabledDependency) {
		t.Fatalf("err = %v", err)
	}
	_, err = New(load(t, "store: {dsn: x, pol: 1}\n"), Modules(storeModule(&ev)))
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("unknown key: err = %v", err)
	}
}

func TestProductionRefusesWeakSettingsUnlessAccepted(t *testing.T) {
	var ev events
	weak := "store: {dsn: 'postgres://h/db?sslmode=disable'}\n"
	_, err := New(load(t, "env: prd\n"+weak), Modules(storeModule(&ev)))
	if err == nil || !strings.Contains(err.Error(), "prd_exceptions.database_plaintext") {
		t.Fatalf("err = %v", err)
	}
	a, err := New(load(t, "env: prd\nprd_exceptions: {database_plaintext: true}\n"+weak), Modules(storeModule(&ev)))
	if err != nil {
		t.Fatal(err)
	}
	if w := a.Warnings(); len(w) != 1 || !strings.Contains(w[0], "disables TLS") {
		t.Fatalf("warnings = %v", w)
	}
	a, err = New(load(t, weak), Modules(storeModule(&ev)))
	if err != nil {
		t.Fatal("outside prd a weak setting is a warning:", err)
	}
	var buf bytes.Buffer
	a.opts.logger = slog.New(slog.NewTextHandler(&buf, nil))
	if err := a.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = a.Shutdown(context.Background())
	if !strings.Contains(buf.String(), "disables TLS") {
		t.Fatalf("the warning was not logged: %s", buf.String())
	}
}

// A failed start rolls back and is reported through the logger; the error is
// returned, never an exit.
func TestStartFailureRollsBack(t *testing.T) {
	var ev events
	boom := errors.New("bind: address already in use")
	listener := module.New("listener", module.Definition{Requires: []module.Handle{storeCap}, Ingress: true,
		Init: func(context.Context, *module.Scope) (module.Hooks, error) {
			return module.Hooks{Start: func(context.Context) error { return boom }}, nil
		}})
	var buf bytes.Buffer
	a, err := New(load(t, ""), Modules(storeModule(&ev), listener), WithLogger(slog.New(slog.NewTextHandler(&buf, nil))))
	if err != nil {
		t.Fatal(err)
	}
	err = a.Start(context.Background())
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if got := ev.get(); !slices.Equal(got, []string{"init store", "stop store"}) {
		t.Fatalf("events = %v", got)
	}
	if !strings.Contains(buf.String(), "[Fatal ERROR] bind: address already in use") {
		t.Fatalf("log = %s", buf.String())
	}
	if err := a.Run(context.Background()); !errors.Is(err, module.ErrState) {
		t.Fatalf("run after a failed start: %v", err)
	}
}

func TestConcurrentShutdownAndTimeout(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	stuck := module.New("stuck", module.Definition{Init: func(context.Context, *module.Scope) (module.Hooks, error) {
		return module.Hooks{Stop: func(context.Context) error { <-block; return nil }}, nil
	}})
	a, err := New(load(t, ""), Modules(stuck))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	began := time.Now()
	for range 8 {
		wg.Go(func() { errs <- a.Shutdown(ctx) })
	}
	wg.Wait()
	close(errs)
	if elapsed := time.Since(began); elapsed > 3*time.Second {
		t.Fatalf("shutdown overran its deadline: %v", elapsed)
	}
	for err := range errs {
		if !errors.Is(err, module.ErrStopTimeout) {
			t.Fatalf("err = %v", err)
		}
	}
}

// Two applications in one process share no capability and no lifecycle.
func TestIndependentApplications(t *testing.T) {
	var ev1, ev2 events
	cfg := load(t, "store: {pool: 2}\n")
	a, err := New(cfg, Modules(storeModule(&ev1)))
	if err != nil {
		t.Fatal(err)
	}
	b, err := New(cfg, Modules(storeModule(&ev2)))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := b.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	sa, _ := Value(a, storeCap)
	sb, _ := Value(b, storeCap)
	if sa == sb || sa.cfg.Pool != 2 {
		t.Fatal("the applications share their store")
	}
	_ = a.Shutdown(context.Background())
	if err := sb.use(); err != nil {
		t.Fatal("stopping one application closed the other's store")
	}
	_ = b.Shutdown(context.Background())
}

// The logging module's logger replaces the bootstrap one once published.
func TestPublishedLoggerIsUsed(t *testing.T) {
	var boot, configured bytes.Buffer
	logger := module.New("logger", module.Definition{Provides: []module.Handle{loggerCapability},
		Init: func(_ context.Context, s *module.Scope) (module.Hooks, error) {
			module.Provide(s, loggerCapability, slog.New(slog.NewTextHandler(&configured, nil)))
			return module.Hooks{}, nil
		}})
	a, err := New(load(t, ""), Modules(logger), WithLogger(slog.New(slog.NewTextHandler(&boot, nil))))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = a.Shutdown(context.Background())
	if !strings.Contains(configured.String(), "Application started") || strings.Contains(boot.String(), "Application started") {
		t.Fatalf("boot %q configured %q", boot.String(), configured.String())
	}
}
