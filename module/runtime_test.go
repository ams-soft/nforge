package module

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type recorder struct {
	mu     sync.Mutex
	events []string
}

func (r *recorder) add(e string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *recorder) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.events)
}

type spec struct {
	id                  ID
	requires            []ID
	ingress             bool
	failInit, failStart error
	failStop            error
}

// tracked records every hook of the module described by sp.
func (r *recorder) tracked(sp spec) Descriptor {
	c := capOf(sp.id)
	return New(sp.id, Definition{
		Provides: []Handle{c}, Requires: handles(sp.requires...), Ingress: sp.ingress,
		Init: func(_ context.Context, s *Scope) (Hooks, error) {
			r.add("init " + string(sp.id))
			if sp.failInit != nil {
				return Hooks{}, sp.failInit
			}
			Provide(s, c, string(sp.id))
			return Hooks{
				Start: func(context.Context) error { r.add("start " + string(sp.id)); return sp.failStart },
				Stop:  func(context.Context) error { r.add("stop " + string(sp.id)); return sp.failStop },
			}, nil
		},
	})
}

func run(t *testing.T, opts Options, mods ...Descriptor) (*Runtime, error) {
	t.Helper()
	plan, err := build(mods...)
	if err != nil {
		t.Fatal(err)
	}
	rt := plan.NewRuntime(opts)
	return rt, rt.Start(context.Background())
}

// Every Init finishes before the first Start, and ingress modules start last,
// so no request or job can arrive before every handler is registered.
func TestEveryInitBeforeAnyStartAndIngressLast(t *testing.T) {
	var r recorder
	rt, err := run(t, Options{},
		r.tracked(spec{id: "db"}),
		r.tracked(spec{id: "http", requires: []ID{"db"}, ingress: true}),
		r.tracked(spec{id: "orders", requires: []ID{"db", "http"}}),
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"init db", "init http", "init orders", "start db", "start orders", "start http"}
	if got := r.list(); !slices.Equal(got, want) {
		t.Fatalf("events = %v", got)
	}
	if rt.Phase() != PhaseStarted {
		t.Fatalf("phase = %s", rt.Phase())
	}
	if err := rt.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	want = append(want, "stop http", "stop orders", "stop db")
	if got := r.list(); !slices.Equal(got, want) {
		t.Fatalf("events = %v", got)
	}
	if rt.Phase() != PhaseStopped {
		t.Fatalf("phase = %s", rt.Phase())
	}
}

func TestShutdownJoinsErrorsAndRunsEveryHook(t *testing.T) {
	var r recorder
	errB, errA := errors.New("stop b"), errors.New("stop a")
	rt, err := run(t, Options{},
		r.tracked(spec{id: "a", failStop: errA}), off(r.tracked(spec{id: "off"})),
		r.tracked(spec{id: "b", requires: []ID{"a"}, failStop: errB}), r.tracked(spec{id: "c", requires: []ID{"b"}}),
	)
	if err != nil {
		t.Fatal(err)
	}
	err = rt.Shutdown(context.Background())
	if !errors.Is(err, errA) || !errors.Is(err, errB) {
		t.Fatalf("err = %v", err)
	}
	var le *LifecycleError
	if !errors.As(err, &le) || le.Phase != PhaseStop {
		t.Fatalf("err = %#v", err)
	}
	stops := slices.DeleteFunc(r.list(), func(e string) bool { return !strings.HasPrefix(e, "stop") })
	if want := []string{"stop c", "stop b", "stop a"}; !slices.Equal(stops, want) {
		t.Fatalf("stops = %v", stops)
	}
}

func TestInitFailureRollsBackWithoutStarting(t *testing.T) {
	var r recorder
	boom, stopErr := errors.New("boom"), errors.New("stop a")
	var cause error
	var usable bool
	plan, err := build(
		r.tracked(spec{id: "a", failStop: stopErr}), r.tracked(spec{id: "b", requires: []ID{"a"}}),
		r.tracked(spec{id: "c", requires: []ID{"b"}, failInit: boom}), r.tracked(spec{id: "d", requires: []ID{"c"}}),
	)
	if err != nil {
		t.Fatal(err)
	}
	var rt *Runtime
	rt = plan.NewRuntime(Options{OnFailure: func(err error) {
		cause = err
		_, usable = Value(rt, capOf("a"))
		r.add("reported")
	}})
	err = rt.Start(context.Background())
	if !errors.Is(err, boom) || !errors.Is(err, stopErr) {
		t.Fatalf("err = %v", err)
	}
	var le *LifecycleError
	if !errors.As(cause, &le) || le.Module != "c" || le.Phase != PhaseInit || !usable {
		t.Fatalf("cause = %v, usable = %v", cause, usable)
	}
	want := []string{"init a", "init b", "init c", "reported", "stop b", "stop a"}
	if got := r.list(); !slices.Equal(got, want) {
		t.Fatalf("events = %v", got)
	}
	states := map[ID]State{}
	for _, s := range rt.Status(context.Background()).Modules {
		states[s.ID] = s.State
	}
	if states["a"] != StateStopped || states["c"] != StateFailed || states["d"] != StatePending {
		t.Fatalf("states = %v", states)
	}
	if rt.Phase() != PhaseFailed {
		t.Fatalf("phase = %s", rt.Phase())
	}
	if err := rt.Start(context.Background()); !errors.Is(err, ErrState) {
		t.Fatalf("restart: err = %v", err)
	}
}

// A failed Start stops everything that was initialized, the modules that never
// started included, and the ingress module is never started.
func TestStartFailureStopsEveryInitializedModule(t *testing.T) {
	var r recorder
	boom := errors.New("bind: address in use")
	_, err := run(t, Options{},
		r.tracked(spec{id: "db"}),
		r.tracked(spec{id: "broker", requires: []ID{"db"}, failStart: boom}),
		r.tracked(spec{id: "late", requires: []ID{"broker"}}),
		r.tracked(spec{id: "http", requires: []ID{"db"}, ingress: true}),
	)
	var le *LifecycleError
	if !errors.Is(err, boom) || !errors.As(err, &le) || le.Phase != PhaseStart || le.Module != "broker" {
		t.Fatalf("err = %v", err)
	}
	want := []string{"init db", "init broker", "init late", "init http", "start db", "start broker",
		"stop http", "stop late", "stop broker", "stop db"}
	if got := r.list(); !slices.Equal(got, want) {
		t.Fatalf("events = %v", got)
	}
}

func TestUnpublishedCapabilityIsAnInitError(t *testing.T) {
	stopped := false
	m := New("a", Definition{Provides: handles("c"), Init: func(context.Context, *Scope) (Hooks, error) {
		return Hooks{Stop: func(context.Context) error { stopped = true; return nil }}, nil
	}})
	if _, err := run(t, Options{}, m); !errors.Is(err, ErrNotProvided) {
		t.Fatalf("err = %v", err)
	}
	if !stopped {
		t.Fatal("the module was initialized, so it must be rolled back")
	}
}

// A module sees only what it declared, with the declared type; misuse fails the
// start instead of the process.
func TestScopeIsRestricted(t *testing.T) {
	other := capOf("other")
	absent := capOf("absent")
	var kept *Scope
	reader := New("reader", Definition{
		Requires: []Handle{other}, Optional: []Handle{absent},
		Init: func(_ context.Context, s *Scope) (Hooks, error) {
			kept = s
			if v := Require(s, other); v != "other" {
				t.Errorf("Require = %q", v)
			}
			if _, ok := Lookup(s, absent); ok {
				t.Error("absent optional capability")
			}
			return Hooks{}, nil
		},
	})
	rt, err := run(t, Options{}, mod("secret"), mod("other"), reader)
	if err != nil {
		t.Fatal(err)
	}
	mustPanic(t, "without declaring", func() { Lookup(kept, capOf("secret")) })
	mustPanic(t, "as int, but declared it as string", func() { Lookup(kept, NewCapability[int]("other")) })
	mustPanic(t, "does not declare", func() { Provide(&Scope{rt: kept.rt, u: kept.u}, capOf("secret"), "x") })
	mustPanic(t, "after Init", func() { Provide(kept, capOf("reader"), "x") })
	if v, ok := Value(rt, NewCapability[int]("other")); ok || v != 0 {
		t.Fatal("Value with another type must not cast")
	}

	undeclared := New("sneaky", Definition{Init: func(_ context.Context, s *Scope) (Hooks, error) {
		Require(s, capOf("secret"))
		return Hooks{}, nil
	}})
	if _, err := run(t, Options{}, mod("secret"), undeclared); err == nil || !strings.Contains(err.Error(), "init panicked") {
		t.Fatalf("undeclared access: err = %v", err)
	}
	wrongType := New("liar", Definition{Provides: handles("v"), Init: func(_ context.Context, s *Scope) (Hooks, error) {
		Provide(s, NewCapability[int]("v"), 1)
		return Hooks{}, nil
	}})
	if _, err := run(t, Options{}, wrongType); err == nil || !strings.Contains(err.Error(), "declared it as string") {
		t.Fatalf("wrong published type: err = %v", err)
	}
}

func mustPanic(t *testing.T, text string, fn func()) {
	t.Helper()
	defer func() {
		r := recover()
		if r == nil || !strings.Contains(fmt.Sprint(r), text) {
			t.Fatalf("panic = %v, want it to contain %q", r, text)
		}
	}()
	fn()
}

// The rollback gets a live, bounded context even when the start context was
// canceled, or every Stop would give up at once.
func TestRollbackContextIsIndependent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var rollbackErr error
	var deadline bool
	a := New("a", Definition{Init: func(context.Context, *Scope) (Hooks, error) {
		return Hooks{Stop: func(c context.Context) error {
			rollbackErr = c.Err()
			_, deadline = c.Deadline()
			return nil
		}}, nil
	}})
	b := New("b", Definition{Init: func(c context.Context, _ *Scope) (Hooks, error) {
		cancel()
		return Hooks{}, c.Err()
	}})
	plan, err := build(a, b)
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.NewRuntime(Options{RollbackTimeout: time.Second}).Start(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if rollbackErr != nil || !deadline {
		t.Fatalf("rollback ctx: err %v, deadline %v", rollbackErr, deadline)
	}
}

func TestCanceledStartInitializesNothing(t *testing.T) {
	var r recorder
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	plan, err := build(r.tracked(spec{id: "a"}))
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.NewRuntime(Options{}).Start(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if got := r.list(); len(got) != 0 {
		t.Fatalf("events = %v", got)
	}
}

// Shutdown during a start cancels it, waits for it, and stops what was
// initialized exactly once.
func TestShutdownWhileStarting(t *testing.T) {
	var r recorder
	entered := make(chan struct{})
	slow := New("slow", Definition{Requires: handles("db"), Init: func(ctx context.Context, _ *Scope) (Hooks, error) {
		close(entered)
		<-ctx.Done()
		return Hooks{}, ctx.Err()
	}})
	plan, err := build(r.tracked(spec{id: "db"}), slow, r.tracked(spec{id: "after", requires: []ID{"db"}}))
	if err != nil {
		t.Fatal(err)
	}
	rt := plan.NewRuntime(Options{})
	startErr := make(chan error, 1)
	go func() { startErr <- rt.Start(context.Background()) }()
	<-entered
	if err := rt.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-startErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("start err = %v", err)
	}
	if got, want := r.list(), []string{"init db", "stop db"}; !slices.Equal(got, want) {
		t.Fatalf("events = %v", got)
	}
	if rt.Phase() != PhaseStopped {
		t.Fatalf("phase = %s", rt.Phase())
	}
	if err := rt.Start(context.Background()); !errors.Is(err, ErrState) {
		t.Fatalf("start after shutdown: err = %v", err)
	}
}

func TestShutdownBeforeStart(t *testing.T) {
	var r recorder
	plan, err := build(r.tracked(spec{id: "a"}))
	if err != nil {
		t.Fatal(err)
	}
	rt := plan.NewRuntime(Options{})
	if err := rt.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := rt.Start(context.Background()); !errors.Is(err, ErrState) {
		t.Fatalf("err = %v", err)
	}
	if got := r.list(); len(got) != 0 {
		t.Fatalf("events = %v", got)
	}
}

func TestConcurrentShutdownRunsHooksOnce(t *testing.T) {
	var runs atomic.Int32
	m := New("a", Definition{Init: func(context.Context, *Scope) (Hooks, error) {
		return Hooks{Stop: func(context.Context) error { runs.Add(1); return errors.New("once") }}, nil
	}})
	rt, err := run(t, Options{}, m)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var sameErr atomic.Int32
	for range 32 {
		wg.Go(func() {
			_ = rt.Status(context.Background())
			if err := rt.Shutdown(context.Background()); err != nil && strings.Contains(err.Error(), "once") {
				sameErr.Add(1)
			}
		})
	}
	wg.Wait()
	if got := runs.Load(); got != 1 {
		t.Fatalf("Stop ran %d times", got)
	}
	if sameErr.Load() != 32 {
		t.Fatalf("%d callers got the shutdown error", sameErr.Load())
	}
}

// Mixed HTTP + worker application: both ingress modules drain before the
// domain and its database close, and the database is never closed while an
// in-flight request or job still uses it.
func TestMixedIngressDrainsBeforeResourcesClose(t *testing.T) {
	var (
		dbClosed atomic.Bool
		misuse   atomic.Int32
		drained  sync.WaitGroup
		inFlight = make(chan struct{})
		release  = make(chan struct{})
		r        recorder
		dbCap    = capOf("db")
		httpCap  = capOf("http")
		queueCap = capOf("queue")
		useDB    = func() {
			if dbClosed.Load() {
				misuse.Add(1)
			}
		}
	)
	db := New("db", Definition{Provides: []Handle{dbCap}, Init: func(_ context.Context, s *Scope) (Hooks, error) {
		Provide(s, dbCap, "pool")
		return Hooks{Stop: func(context.Context) error { r.add("stop db"); dbClosed.Store(true); return nil }}, nil
	}})
	// an ingress module whose Stop waits for its in-flight work
	ingress := func(id ID, c Capability[string]) Descriptor {
		return New(id, Definition{Provides: []Handle{c}, Ingress: true, Init: func(_ context.Context, s *Scope) (Hooks, error) {
			Provide(s, c, string(id))
			var work sync.WaitGroup
			return Hooks{
				Start: func(context.Context) error {
					drained.Add(1)
					work.Go(func() {
						defer drained.Done()
						inFlight <- struct{}{}
						<-release // the request/job runs until shutdown
						time.Sleep(10 * time.Millisecond)
						useDB()
					})
					return nil
				},
				Stop: func(context.Context) error { close2(release); work.Wait(); r.add("stop " + string(id)); return nil },
			}, nil
		}})
	}
	domain := New("orders", Definition{Requires: []Handle{dbCap, httpCap, queueCap}, Init: func(_ context.Context, s *Scope) (Hooks, error) {
		Require(s, httpCap) // registers its routes
		Require(s, queueCap)
		return Hooks{Stop: func(context.Context) error { useDB(); r.add("stop orders"); return nil }}, nil
	}})
	rt, err := run(t, Options{}, db, ingress("http", httpCap), ingress("worker", queueCap), domain)
	if err != nil {
		t.Fatal(err)
	}
	<-inFlight
	<-inFlight
	if err := rt.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if misuse.Load() != 0 {
		t.Fatal("the database was closed while work still used it")
	}
	events := r.list()
	if len(events) != 4 || !slices.Contains(events[:2], "stop http") || !slices.Contains(events[:2], "stop worker") ||
		events[2] != "stop orders" || events[3] != "stop db" {
		t.Fatalf("events = %v", events)
	}
}

var closeMu sync.Mutex

// close2 closes ch once, for the two ingress modules sharing a release channel.
func close2(ch chan struct{}) {
	closeMu.Lock()
	defer closeMu.Unlock()
	select {
	case <-ch:
	default:
		close(ch)
	}
}

// Independent modules stop concurrently, dependents still before providers.
func TestIndependentModulesStopConcurrently(t *testing.T) {
	both := make(chan struct{}, 2)
	wait := func(id ID) Descriptor {
		return New(id, Definition{Requires: handles("base"), Init: func(context.Context, *Scope) (Hooks, error) {
			return Hooks{Stop: func(ctx context.Context) error {
				both <- struct{}{}
				for len(both) < 2 {
					select {
					case <-ctx.Done():
						return errors.New("the other module did not stop concurrently")
					case <-time.After(time.Millisecond):
					}
				}
				return nil
			}}, nil
		}})
	}
	rt, err := run(t, Options{}, mod("base"), wait("x"), wait("y"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := rt.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}

// The deadline is honored: a Stop that never returns is abandoned after the one
// shared grace window, and what it depends on is left open, not closed under it.
func TestHungStopIsAbandonedAndItsDependenciesStayOpen(t *testing.T) {
	var r recorder
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	hung := New("hung", Definition{Requires: handles("db"), Init: func(context.Context, *Scope) (Hooks, error) {
		return Hooks{Stop: func(context.Context) error { <-block; return nil }}, nil
	}})
	rt, err := run(t, Options{StopGrace: 20 * time.Millisecond}, r.tracked(spec{id: "db"}), hung, r.tracked(spec{id: "free"}))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	began := time.Now()
	err = rt.Shutdown(ctx)
	if elapsed := time.Since(began); elapsed > 500*time.Millisecond {
		t.Fatalf("shutdown took %v, past its deadline and grace", elapsed)
	}
	if !errors.Is(err, ErrStopTimeout) || !strings.Contains(err.Error(), "db left open because hung did not stop") {
		t.Fatalf("err = %v", err)
	}
	if got := r.list(); slices.Contains(got, "stop db") || !slices.Contains(got, "stop free") {
		t.Fatalf("events = %v", got)
	}
}

// A consumer that hangs until the deadline must not cost its providers their
// stop: logs and traces flush last, and an expired context would drop them.
func TestHookAfterTheDeadlineGetsTheGraceContext(t *testing.T) {
	var flushErr error
	flushed := false
	logs := New("logs", Definition{Provides: handles("logs"), Init: func(_ context.Context, s *Scope) (Hooks, error) {
		Provide(s, capOf("logs"), "l")
		return Hooks{Stop: func(c context.Context) error {
			flushed, flushErr = true, c.Err()
			return nil
		}}, nil
	}})
	slow := New("slow", Definition{Requires: handles("logs"), Init: func(context.Context, *Scope) (Hooks, error) {
		return Hooks{Stop: func(c context.Context) error {
			<-c.Done()
			return c.Err()
		}}, nil
	}})
	rt, err := run(t, Options{StopGrace: time.Second}, logs, slow)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := rt.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	if !flushed || flushErr != nil {
		t.Fatalf("flushed = %v with ctx error %v", flushed, flushErr)
	}
}

func TestRuntimeFailureIsReportedAndNotReady(t *testing.T) {
	var report func(error)
	listener := New("listener", Definition{Ingress: true, Init: func(_ context.Context, s *Scope) (Hooks, error) {
		report = s.ReportFailure
		return Hooks{}, nil
	}})
	rt, err := run(t, Options{}, listener, mod("other"))
	if err != nil {
		t.Fatal(err)
	}
	if r := rt.Status(context.Background()); !r.Ready {
		t.Fatalf("report = %+v", r)
	}
	died := errors.New("accept: too many open files")
	report(died)
	select {
	case <-rt.Failed():
	case <-time.After(time.Second):
		t.Fatal("Failed not closed")
	}
	var le *LifecycleError
	if err := rt.Err(); !errors.Is(err, died) || !errors.As(err, &le) || le.Module != "listener" || le.Phase != PhaseRun {
		t.Fatalf("Err = %v", err)
	}
	r := rt.Status(context.Background())
	if r.Ready {
		t.Fatal("a failed module must not report ready")
	}
	for _, m := range r.Modules {
		if m.ID == "listener" && (m.State != StateFailed || m.Availability != NotReady) {
			t.Fatalf("listener = %+v", m)
		}
	}
	if err := rt.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	report(errors.New("after shutdown")) // ignored
}

// A failure reported during the start fails it.
func TestFailureDuringStartFailsTheStart(t *testing.T) {
	early := New("early", Definition{Init: func(_ context.Context, s *Scope) (Hooks, error) {
		return Hooks{Start: func(context.Context) error { s.ReportFailure(errors.New("lost")); return nil }}, nil
	}})
	if _, err := run(t, Options{}, early, mod("next")); err == nil || !strings.Contains(err.Error(), "lost") {
		t.Fatalf("err = %v", err)
	}
}

func TestReadinessRecoversAndProbesAreShared(t *testing.T) {
	var down atomic.Bool
	var probes atomic.Int32
	gate := make(chan struct{})
	db := New("db", Definition{Init: func(context.Context, *Scope) (Hooks, error) {
		return Hooks{Ready: func(context.Context) error {
			probes.Add(1)
			<-gate
			if down.Load() {
				return errors.New("down")
			}
			return nil
		}}, nil
	}})
	optional := New("metrics", Definition{NonCritical: true, Init: func(context.Context, *Scope) (Hooks, error) {
		return Hooks{Ready: func(context.Context) error { return errors.New("exporter down") }}, nil
	}})
	rt, err := run(t, Options{ProbeInterval: time.Hour}, db, optional)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	reports := make(chan Report, 16)
	for range 16 {
		wg.Go(func() { reports <- rt.Status(context.Background()) })
	}
	time.Sleep(20 * time.Millisecond)
	close(gate)
	wg.Wait()
	close(reports)
	if n := probes.Load(); n != 1 {
		t.Fatalf("%d probes for 16 concurrent callers", n)
	}
	for r := range reports {
		if !r.Ready {
			t.Fatalf("a non-critical module must not make the application not ready: %+v", r)
		}
	}

	// a fresh runtime with no reuse: an outage and its recovery show at once
	rt2 := rt.plan.NewRuntime(Options{ProbeInterval: time.Nanosecond})
	if err := rt2.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	down.Store(true)
	if rt2.Status(context.Background()).Ready {
		t.Fatal("ready while the database is down")
	}
	down.Store(false)
	time.Sleep(time.Millisecond)
	if !rt2.Status(context.Background()).Ready {
		t.Fatal("not ready after the database recovered")
	}
	_ = rt.Shutdown(context.Background())
	_ = rt2.Shutdown(context.Background())
	if r := rt.Status(context.Background()); r.Phase != PhaseStopped || r.Ready {
		t.Fatalf("after shutdown: %+v", r)
	}
}

func TestStatus(t *testing.T) {
	hook := func(ready func(context.Context) error) Definition {
		return Definition{Init: func(context.Context, *Scope) (Hooks, error) { return Hooks{Ready: ready}, nil }}
	}
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	rt, err := run(t, Options{ProbeTimeout: 50 * time.Millisecond},
		New("broken", hook(func(context.Context) error { return errors.New("down") })),
		New("fine", hook(func(context.Context) error { return nil })),
		New("hung", hook(func(context.Context) error { <-release; return nil })),
		New("panics", hook(func(context.Context) error { panic("probe bug") })),
		off(New("off", Definition{})),
		New("passive", Definition{}),
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []Status{
		{"broken", StateStarted, NotReady, true},
		{"fine", StateStarted, Ready, true},
		{"hung", StateStarted, NotReady, true},
		{"off", StateDisabled, Disabled, true},
		{"panics", StateStarted, NotReady, true},
		{"passive", StateStarted, Ready, true},
	}
	r := rt.Status(context.Background())
	if !slices.Equal(r.Modules, want) || r.Ready || r.Phase != PhaseStarted {
		t.Fatalf("report = %+v", r)
	}
}

// Capabilities are per application: two runtimes of one plan share nothing.
func TestIndependentRuntimes(t *testing.T) {
	var n atomic.Int32
	counter := New("counter", Definition{Provides: handles("counter"), Init: func(_ context.Context, s *Scope) (Hooks, error) {
		Provide(s, capOf("counter"), fmt.Sprint(n.Add(1)))
		return Hooks{}, nil
	}})
	plan, err := build(counter)
	if err != nil {
		t.Fatal(err)
	}
	a, b := plan.NewRuntime(Options{}), plan.NewRuntime(Options{})
	if err := a.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := b.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	va, _ := Value(a, capOf("counter"))
	vb, _ := Value(b, capOf("counter"))
	if va == vb {
		t.Fatalf("both runtimes share %q", va)
	}
	_ = a.Shutdown(context.Background())
	if b.Phase() != PhaseStarted {
		t.Fatal("stopping one runtime stopped the other")
	}
	_ = b.Shutdown(context.Background())
}

func TestReloadRunsInOrder(t *testing.T) {
	var r recorder
	reloadable := func(id ID, requires ...ID) Descriptor {
		return with(mod(id, requires...), func(d *Definition) {
			init := d.Init
			d.Init = func(ctx context.Context, s *Scope) (Hooks, error) {
				h, err := init(ctx, s)
				h.Reload = func(context.Context) error { r.add("reload " + string(id)); return nil }
				return h, err
			}
		})
	}
	rt, err := run(t, Options{}, reloadable("http", "jose"), reloadable("jose"))
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, want := r.list(), []string{"reload jose", "reload http"}; !slices.Equal(got, want) {
		t.Fatalf("events = %v", got)
	}
	_ = rt.Shutdown(context.Background())
	if err := rt.Reload(context.Background()); !errors.Is(err, ErrState) {
		t.Fatalf("reload after shutdown: err = %v", err)
	}
}

func TestLifetimeContextEndsAfterShutdown(t *testing.T) {
	var life context.Context
	m := New("bg", Definition{Init: func(_ context.Context, s *Scope) (Hooks, error) {
		life = s.Context()
		return Hooks{Stop: func(context.Context) error {
			if life.Err() != nil {
				return errors.New("the lifetime context ended before Stop")
			}
			return nil
		}}, nil
	}})
	rt, err := run(t, Options{}, m)
	if err != nil {
		t.Fatal(err)
	}
	if life.Err() != nil {
		t.Fatal("the lifetime context ended with the start")
	}
	if err := rt.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if life.Err() == nil {
		t.Fatal("the lifetime context outlived the shutdown")
	}
}

// G26: a Ready hook that blocks ignoring its context is not started again by
// the next samples, so probes cannot pile up goroutines behind it.
func TestBlockedReadyHookIsNotProbedAgainWhileRunning(t *testing.T) {
	var started atomic.Int32
	release := make(chan struct{})
	m := New("stuck", Definition{Init: func(context.Context, *Scope) (Hooks, error) {
		return Hooks{Ready: func(context.Context) error {
			started.Add(1)
			<-release // ignores ctx
			return nil
		}}, nil
	}})
	rt, err := run(t, Options{ProbeInterval: time.Nanosecond, ProbeTimeout: 20 * time.Millisecond}, m)
	if err != nil {
		t.Fatal(err)
	}
	for range 30 {
		if rt.Status(context.Background()).Ready {
			t.Fatal("a blocked probe must read as not ready")
		}
		time.Sleep(2 * time.Millisecond)
	}
	if n := started.Load(); n != 1 {
		t.Fatalf("the hook was started %d times while it was still running", n)
	}
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for !rt.Status(context.Background()).Ready {
		if time.Now().After(deadline) {
			t.Fatal("the module never became ready after the hook returned")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
