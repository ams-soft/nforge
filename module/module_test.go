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

type cfg struct{ off map[ID]bool }

func caps(ids ...CapabilityID) func(cfg) []CapabilityID {
	return func(cfg) []CapabilityID { return ids }
}

func enabled(id ID) func(cfg) bool {
	return func(c cfg) bool { return !c.off[id] }
}

// mod is a module providing a capability named after itself.
func mod(id ID, requires ...CapabilityID) Module[cfg] {
	c := NewCapability[string](CapabilityID(id))
	return Module[cfg]{
		ID: id, Provides: []CapabilityID{c.ID()}, Enabled: enabled(id), Requires: caps(requires...),
		Init: func(_ context.Context, _ cfg, s *Scope) (Hooks, error) {
			Provide(s, c, string(id))
			return Hooks{}, nil
		},
	}
}

func TestResolveOrdersDependenciesFirst(t *testing.T) {
	plan, err := Resolve(cfg{}, []Module[cfg]{mod("c", "b"), mod("b", "a"), mod("a")})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := plan.Snapshot().Order, []ID{"a", "b", "c"}; !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

// Independent modules keep the registration order, so one configuration always
// gives one plan.
func TestResolveIsDeterministic(t *testing.T) {
	mods := []Module[cfg]{mod("z"), mod("m"), mod("a"), mod("k", "a"), mod("b", "z")}
	want := []ID{"z", "m", "a", "k", "b"}
	for range 100 {
		plan, err := Resolve(cfg{}, mods)
		if err != nil {
			t.Fatal(err)
		}
		if got := plan.Snapshot().Order; !slices.Equal(got, want) {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

func TestResolveErrors(t *testing.T) {
	optional := mod("x")
	optional.Optional = caps("x")
	tests := []struct {
		name string
		mods []Module[cfg]
		cfg  cfg
		want error
		text string
	}{
		{name: "duplicate id", mods: []Module[cfg]{mod("a"), mod("a")}, want: ErrDuplicateModule, text: `"a"`},
		{name: "invalid id", mods: []Module[cfg]{mod("<script>")}, want: ErrInvalidModule},
		{name: "empty id", mods: []Module[cfg]{mod("")}, want: ErrInvalidModule},
		{name: "invalid capability", mods: []Module[cfg]{{ID: "a", Provides: []CapabilityID{"A B"}}}, want: ErrInvalidModule},
		{name: "capability twice", mods: []Module[cfg]{{ID: "a", Provides: []CapabilityID{"c", "c"}}}, want: ErrInvalidModule},
		{
			name: "ambiguous provider",
			mods: []Module[cfg]{{ID: "a", Provides: []CapabilityID{"c"}}, {ID: "b", Provides: []CapabilityID{"c"}}},
			want: ErrAmbiguousProvider, text: `"a" and "b"`,
		},
		{name: "missing", mods: []Module[cfg]{mod("a", "nope")}, want: ErrMissingDependency, text: `"nope"`},
		{
			name: "disabled", mods: []Module[cfg]{mod("a", "b"), mod("b")}, cfg: cfg{off: map[ID]bool{"b": true}},
			want: ErrDisabledDependency, text: `requires capability "b", but its provider b is disabled`,
		},
		{name: "self", mods: []Module[cfg]{mod("a", "a")}, want: ErrCycle, text: "a -> a"},
		{name: "cycle", mods: []Module[cfg]{mod("a", "b"), mod("b", "c"), mod("c", "a")}, want: ErrCycle, text: "a -> b -> c -> a"},
		{name: "cycle behind a module", mods: []Module[cfg]{mod("z", "a"), mod("a", "b"), mod("b", "a")}, want: ErrCycle, text: "a -> b -> a"},
		{name: "optional cycle", mods: []Module[cfg]{optional}, want: ErrCycle},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Resolve(tt.cfg, tt.mods)
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			if !strings.Contains(err.Error(), tt.text) {
				t.Fatalf("err = %q, want it to contain %q", err, tt.text)
			}
		})
	}
}

func TestValidID(t *testing.T) {
	for id, want := range map[string]bool{
		"a": true, "a-b_c9": true, strings.Repeat("a", 64): true, strings.Repeat("a", 65): false,
		"": false, "1a": false, "-a": false, "A": false, "a b": false, "a.b": false, `a"`: false, "a\n": false, "é": false,
	} {
		if got := validID(id); got != want {
			t.Errorf("%q = %v", id, got)
		}
	}
}

func TestResolveLimitsModules(t *testing.T) {
	mods := make([]Module[cfg], MaxModules+1)
	for i := range mods {
		mods[i] = Module[cfg]{ID: ID(fmt.Sprintf("m%d", i))}
	}
	if _, err := Resolve(cfg{}, mods); !errors.Is(err, ErrInvalidModule) {
		t.Fatalf("err = %v", err)
	}
	if _, err := Resolve(cfg{}, mods[:MaxModules]); err != nil {
		t.Fatal(err)
	}
}

// Requirements depend on the configuration, and a disabled module's requirements
// do not count at all.
func TestResolveConditionalRequirements(t *testing.T) {
	consumer := mod("consumer")
	consumer.Requires = func(c cfg) []CapabilityID {
		if c.off["needs-nothing"] {
			return nil
		}
		return []CapabilityID{"db"}
	}
	off := func(ids ...ID) cfg {
		c := cfg{off: map[ID]bool{}}
		for _, id := range ids {
			c.off[id] = true
		}
		return c
	}
	mods := []Module[cfg]{mod("db"), consumer}
	if _, err := Resolve(off("db"), mods); !errors.Is(err, ErrDisabledDependency) {
		t.Fatalf("err = %v", err)
	}
	if _, err := Resolve(off("db", "needs-nothing"), mods); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(off("db", "consumer"), mods); err != nil {
		t.Fatal("a disabled module requires nothing:", err)
	}
}

// Two alternatives may be registered as long as one is enabled.
func TestResolveDisabledAlternativeIsNotAmbiguous(t *testing.T) {
	a := Module[cfg]{ID: "a", Provides: []CapabilityID{"c"}, Enabled: enabled("a")}
	b := Module[cfg]{ID: "b", Provides: []CapabilityID{"c"}, Enabled: enabled("b")}
	plan, err := Resolve(cfg{off: map[ID]bool{"a": true}}, []Module[cfg]{a, b, mod("user", "c")})
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.Snapshot().Edges; len(got) != 1 || got[0].To != "b" {
		t.Fatalf("edges = %+v", got)
	}
}

func TestOptionalDependency(t *testing.T) {
	user := mod("user")
	user.Optional = caps("extra", "unregistered")
	// the optional provider is registered after its consumer: it still starts first
	mods := []Module[cfg]{user, mod("extra")}
	plan, err := Resolve(cfg{}, mods)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := plan.Snapshot().Order, []ID{"extra", "user"}; !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	plan, err = Resolve(cfg{off: map[ID]bool{"extra": true}}, mods)
	if err != nil {
		t.Fatal("a disabled optional provider is not an error:", err)
	}
	if got := plan.Snapshot().Edges; len(got) != 0 {
		t.Fatalf("edges = %+v", got)
	}
}

func TestSnapshot(t *testing.T) {
	user := mod("user", "b", "b", "a")
	user.Optional = caps("b", "c")
	plan, err := Resolve(cfg{off: map[ID]bool{"off": true}}, []Module[cfg]{mod("off"), mod("c"), mod("b"), mod("a"), user})
	if err != nil {
		t.Fatal(err)
	}
	snap := plan.Snapshot()
	ids := make([]ID, len(snap.Modules))
	for i, m := range snap.Modules {
		ids[i] = m.ID
	}
	if want := []ID{"a", "b", "c", "off", "user"}; !slices.Equal(ids, want) {
		t.Fatalf("modules = %v", ids)
	}
	if off := snap.Modules[3]; off.Enabled || off.StartupOrder != 0 {
		t.Fatalf("off = %+v", off)
	}
	u := snap.Modules[4]
	if !slices.Equal(u.Dependencies, []ID{"a", "b"}) || !slices.Equal(u.OptionalDependencies, []ID{"c"}) || u.StartupOrder != 4 {
		t.Fatalf("user = %+v", u)
	}
	want := []Edge{{"user", "a", "a", true}, {"user", "b", "b", true}, {"user", "c", "c", false}}
	if !slices.Equal(snap.Edges, want) {
		t.Fatalf("edges = %+v", snap.Edges)
	}
	// a caller cannot reach into the plan
	snap.Modules[4].Dependencies[0] = "changed"
	snap.Order[0] = "changed"
	if again := plan.Snapshot(); again.Modules[4].Dependencies[0] != "a" || again.Order[0] == "changed" {
		t.Fatal("the snapshot must be a copy")
	}
}

type recorder struct {
	mu     sync.Mutex
	events []string
}

func (r *recorder) add(e string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *recorder) tracked(id ID, failInit, failStop error, requires ...CapabilityID) Module[cfg] {
	c := NewCapability[string](CapabilityID(id))
	return Module[cfg]{
		ID: id, Provides: []CapabilityID{c.ID()}, Enabled: enabled(id), Requires: caps(requires...),
		Init: func(_ context.Context, _ cfg, s *Scope) (Hooks, error) {
			r.add("init " + string(id))
			if failInit != nil {
				return Hooks{}, failInit
			}
			Provide(s, c, string(id))
			return Hooks{Shutdown: func(context.Context) error {
				r.add("stop " + string(id))
				return failStop
			}}, nil
		},
	}
}

func TestStartAndShutdownOrder(t *testing.T) {
	var r recorder
	stopErr := errors.New("stop b")
	plan, err := Resolve(cfg{off: map[ID]bool{"off": true}}, []Module[cfg]{
		r.tracked("a", nil, nil), r.tracked("off", nil, nil), r.tracked("b", nil, stopErr, "a"), r.tracked("c", nil, nil, "b"),
	})
	if err != nil {
		t.Fatal(err)
	}
	rt, err := plan.Start(context.Background(), StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.Shutdown(context.Background()); !errors.Is(err, stopErr) {
		t.Fatalf("err = %v", err)
	}
	want := []string{"init a", "init b", "init c", "stop c", "stop b", "stop a"}
	if !slices.Equal(r.events, want) {
		t.Fatalf("events = %v", r.events)
	}
}

func TestFailedStartRollsBack(t *testing.T) {
	var r recorder
	boom, stopErr := errors.New("boom"), errors.New("stop a")
	plan, err := Resolve(cfg{}, []Module[cfg]{
		r.tracked("a", nil, stopErr), r.tracked("b", nil, nil, "a"), r.tracked("c", boom, nil, "b"), r.tracked("d", nil, nil, "c"),
	})
	if err != nil {
		t.Fatal(err)
	}
	var failed error
	var usable bool
	rt, err := plan.Start(context.Background(), StartOptions{OnFailure: func(rt *Runtime, cause error) {
		failed = cause
		_, usable = Value(rt, NewCapability[string]("a"))
		r.add("reported")
	}})
	if !errors.Is(err, boom) || !errors.Is(err, stopErr) {
		t.Fatalf("err = %v", err)
	}
	var init *InitError
	if !errors.As(failed, &init) || init.Module != "c" || !usable {
		t.Fatalf("failed = %v, usable = %v", failed, usable)
	}
	want := []string{"init a", "init b", "init c", "reported", "stop b", "stop a"}
	if !slices.Equal(r.events, want) {
		t.Fatalf("events = %v", r.events)
	}
	got := map[ID]Initialization{}
	for _, s := range rt.Status(context.Background()) {
		got[s.ID] = s.Initialization
	}
	if got["a"] != StateStopped || got["c"] != StateFailed || got["d"] != StatePending {
		t.Fatalf("states = %v", got)
	}
}

// The rollback gets a live, bounded context even when the start context was
// canceled, or every Shutdown would give up at once.
func TestRollbackContextIsIndependent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var rollbackErr error
	var deadline bool
	a := Module[cfg]{ID: "a", Init: func(context.Context, cfg, *Scope) (Hooks, error) {
		return Hooks{Shutdown: func(c context.Context) error {
			rollbackErr = c.Err()
			_, deadline = c.Deadline()
			return nil
		}}, nil
	}}
	b := Module[cfg]{ID: "b", Init: func(c context.Context, _ cfg, _ *Scope) (Hooks, error) {
		cancel()
		return Hooks{}, c.Err()
	}}
	plan, err := Resolve(cfg{}, []Module[cfg]{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plan.Start(ctx, StartOptions{RollbackTimeout: time.Second}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if rollbackErr != nil || !deadline {
		t.Fatalf("rollback ctx: err %v, deadline %v", rollbackErr, deadline)
	}
}

func TestCanceledStartInitializesNothingFurther(t *testing.T) {
	var r recorder
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	plan, err := Resolve(cfg{}, []Module[cfg]{r.tracked("a", nil, nil)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plan.Start(ctx, StartOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if len(r.events) != 0 {
		t.Fatalf("events = %v", r.events)
	}
}

func TestUnpublishedCapabilityIsAStartError(t *testing.T) {
	stopped := false
	m := Module[cfg]{ID: "a", Provides: []CapabilityID{"c"}, Init: func(context.Context, cfg, *Scope) (Hooks, error) {
		return Hooks{Shutdown: func(context.Context) error { stopped = true; return nil }}, nil
	}}
	plan, err := Resolve(cfg{}, []Module[cfg]{m})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plan.Start(context.Background(), StartOptions{}); !errors.Is(err, ErrNotProvided) {
		t.Fatalf("err = %v", err)
	}
	if !stopped {
		t.Fatal("the module started, so it must be rolled back")
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

// A module sees only what it declared, so the graph tells the truth.
func TestScopeIsRestricted(t *testing.T) {
	secret := NewCapability[string]("secret")
	other := NewCapability[string]("other")
	absent := NewCapability[string]("absent")
	var kept *Scope
	reader := Module[cfg]{
		ID: "reader", Optional: caps("absent"), Requires: caps("other"),
		Init: func(_ context.Context, _ cfg, s *Scope) (Hooks, error) {
			kept = s
			if v := Require(s, other); v != "other" {
				t.Errorf("Require = %q", v)
			}
			if _, ok := Lookup(s, absent); ok {
				t.Error("absent optional capability")
			}
			mustPanic(t, "without declaring", func() { Lookup(s, secret) })
			mustPanic(t, "does not declare", func() { Provide(s, secret, "x") })
			return Hooks{}, nil
		},
	}
	plan, err := Resolve(cfg{}, []Module[cfg]{mod("secret"), mod("other"), reader})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plan.Start(context.Background(), StartOptions{}); err != nil {
		t.Fatal(err)
	}
	s := &Scope{rt: kept.rt, id: "reader", provides: []CapabilityID{"late"}}
	s.sealed.Store(true)
	mustPanic(t, "after Init", func() { Provide(s, NewCapability[string]("late"), "x") })
}

// A consumer that hangs until the deadline must not cost its providers their
// shutdown: logs and traces are flushed last, and an expired context drops them.
func TestHungHookLeavesLaterHooksALiveContext(t *testing.T) {
	var flushErr error
	flushed := false
	logs := Module[cfg]{ID: "logs", Provides: []CapabilityID{"logs"}, Init: func(_ context.Context, _ cfg, s *Scope) (Hooks, error) {
		Provide(s, NewCapability[int]("logs"), 1)
		return Hooks{Shutdown: func(c context.Context) error {
			flushed, flushErr = true, c.Err()
			return nil
		}}, nil
	}}
	hung := Module[cfg]{ID: "hung", Requires: caps("logs"), Init: func(context.Context, cfg, *Scope) (Hooks, error) {
		return Hooks{Shutdown: func(c context.Context) error {
			<-c.Done()
			return c.Err()
		}}, nil
	}}
	plan, err := Resolve(cfg{}, []Module[cfg]{logs, hung})
	if err != nil {
		t.Fatal(err)
	}
	rt, err := plan.Start(context.Background(), StartOptions{})
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

func TestConcurrentShutdownRunsHooksOnce(t *testing.T) {
	var runs atomic.Int32
	m := Module[cfg]{ID: "a", Init: func(context.Context, cfg, *Scope) (Hooks, error) {
		return Hooks{Shutdown: func(context.Context) error { runs.Add(1); return nil }}, nil
	}}
	plan, err := Resolve(cfg{}, []Module[cfg]{m})
	if err != nil {
		t.Fatal(err)
	}
	rt, err := plan.Start(context.Background(), StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			_ = rt.Status(context.Background())
			_ = rt.Shutdown(context.Background())
		})
	}
	wg.Wait()
	if got := runs.Load(); got != 1 {
		t.Fatalf("Shutdown ran %d times", got)
	}
}

func TestStatus(t *testing.T) {
	bad := errors.New("down")
	hook := func(ready func(context.Context) error) func(context.Context, cfg, *Scope) (Hooks, error) {
		return func(context.Context, cfg, *Scope) (Hooks, error) { return Hooks{Ready: ready}, nil }
	}
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	mods := []Module[cfg]{
		{ID: "broken", Init: hook(func(context.Context) error { return bad })},
		{ID: "fine", Init: hook(func(context.Context) error { return nil })},
		{ID: "hung", Init: hook(func(context.Context) error { <-release; return nil })},
		{ID: "off", Enabled: func(cfg) bool { return false }},
		{ID: "passive"},
	}
	plan, err := Resolve(cfg{}, mods)
	if err != nil {
		t.Fatal(err)
	}
	rt, err := plan.Start(context.Background(), StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	want := []Status{
		{"broken", StateStarted, NotReady},
		{"fine", StateStarted, Ready},
		{"hung", StateStarted, NotReady},
		{"off", StateDisabled, Disabled},
		{"passive", StateStarted, Ready},
	}
	if got := rt.Status(ctx); !slices.Equal(got, want) {
		t.Fatalf("status = %+v", got)
	}
}

func BenchmarkResolve(b *testing.B) {
	mods := []Module[cfg]{mod("logger"), mod("tracing", "logger"), mod("report", "logger"), mod("codec", "logger"),
		mod("database", "logger"), mod("jose", "logger"), mod("cache", "logger", "codec"), mod("http", "logger", "codec", "cache")}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := Resolve(cfg{}, mods); err != nil {
			b.Fatal(err)
		}
	}
}
