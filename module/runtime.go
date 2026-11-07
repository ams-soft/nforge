package module

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultRollbackTimeout = 5 * time.Second
	defaultStopGrace       = time.Second
	defaultProbeTimeout    = 2 * time.Second
	defaultProbeInterval   = time.Second
)

// State is where one module is in its lifecycle.
type State string

// Module states.
const (
	StateDisabled    State = "disabled"
	StatePending     State = "pending"
	StateInitialized State = "initialized"
	StateStarted     State = "started"
	StateStopping    State = "stopping"
	StateStopped     State = "stopped"
	StateFailed      State = "failed"
)

// Availability is the result of probing a module.
type Availability string

// Availability values. A disabled module is not an unavailable one.
const (
	Ready    Availability = "ready"
	NotReady Availability = "not_ready"
	Disabled Availability = "disabled"
)

// Phase of the whole application, as Runtime.Phase reports it.
type RunPhase string

// Application phases. Ready is not a phase: it is the result of a probe
// (Report.Ready) while the application is started.
const (
	PhaseValidated    RunPhase = "validated" // built, not started
	PhaseInitializing RunPhase = "initializing"
	PhaseStarting     RunPhase = "starting"
	PhaseStarted      RunPhase = "started"
	PhaseStopping     RunPhase = "stopping"
	PhaseStopped      RunPhase = "stopped"
	PhaseFailed       RunPhase = "failed" // the start failed and was rolled back
)

// Status is the operational state of one module. It lives apart from the
// immutable Snapshot: it changes, the structure does not.
type Status struct {
	ID           ID
	State        State
	Availability Availability
	// Critical is false for a NonCritical module, whose readiness does not
	// count in Report.Ready.
	Critical bool
}

// Report is a readiness sample of the application.
type Report struct {
	Phase RunPhase
	// Ready is true only while the application is started, no module failed and
	// every critical enabled module is ready.
	Ready bool
	// Modules holds every registered module, sorted by ID.
	Modules []Status
	// SampledAt is when the probes ran.
	SampledAt time.Time
}

// Options tunes a Runtime. The zero value is valid.
type Options struct {
	// RollbackTimeout bounds the rollback after a failed start; 0 means 5 s. It
	// is independent of the start context, which may already be canceled.
	RollbackTimeout time.Duration
	// StopGrace is the one extra window that the Stop hooks still running or
	// not yet started get, all together, once the shutdown context has ended;
	// 0 means 1 s. Without it a consumer hanging until the deadline would cost
	// the telemetry flush that explains why it hung. It bounds the overrun of
	// Shutdown past its deadline.
	StopGrace time.Duration
	// ProbeTimeout bounds one readiness sample; 0 means 2 s.
	ProbeTimeout time.Duration
	// ProbeInterval is how long a sample is reused, so a burst of readiness
	// requests costs one probe of each dependency; 0 means 1 s.
	ProbeInterval time.Duration
	// OnFailure runs before a rollback, while the modules that were initialized
	// are still usable (a logger, typically). It receives the failure cause.
	OnFailure func(cause error)
	// OnInitialized runs once every module is initialized, before the first
	// Start: the configured logger exists and nothing accepts work yet.
	OnInitialized func()
}

func (o Options) withDefaults() Options {
	if o.RollbackTimeout <= 0 {
		o.RollbackTimeout = defaultRollbackTimeout
	}
	if o.StopGrace <= 0 {
		o.StopGrace = defaultStopGrace
	}
	if o.ProbeTimeout <= 0 {
		o.ProbeTimeout = defaultProbeTimeout
	}
	if o.ProbeInterval <= 0 {
		o.ProbeInterval = defaultProbeInterval
	}
	return o
}

// Runtime runs one Plan once. All its methods are safe for concurrent use.
type Runtime struct {
	plan    *Plan
	opts    Options
	entries map[ID]*entry

	life       context.Context // ends when the shutdown has finished
	lifeCancel context.CancelFunc

	mu          sync.Mutex
	phase       RunPhase
	values      map[CapabilityID]any
	startCancel context.CancelFunc
	startDone   chan struct{}

	stopOnce sync.Once
	stopErr  error

	fatalOnce sync.Once
	fatal     chan struct{}
	fatalErr  error

	probeMu     sync.Mutex
	probeReport *Report
	probing     chan struct{}
}

type entry struct {
	u     *unit
	state atomic.Value // State
	// hooks, initialized and started are written by Start before the phase
	// leaves starting, and read by the shutdown after it waited for Start.
	hooks       Hooks
	initialized bool
	started     bool
}

func (e *entry) load() State { s, _ := e.state.Load().(State); return s }

// NewRuntime returns the runtime of p, not yet started. The same Plan may give
// several independent runtimes.
func (p *Plan) NewRuntime(opts Options) *Runtime {
	life, cancel := context.WithCancel(context.Background())
	rt := &Runtime{
		plan: p, opts: opts.withDefaults(), entries: make(map[ID]*entry, len(p.units)),
		life: life, lifeCancel: cancel,
		phase: PhaseValidated, values: map[CapabilityID]any{}, fatal: make(chan struct{}),
	}
	for _, u := range p.units {
		e := &entry{u: u}
		e.state.Store(StateDisabled)
		if u.enabled {
			e.state.Store(StatePending)
		}
		rt.entries[u.id] = e
	}
	return rt
}

// Phase returns the application phase.
func (rt *Runtime) Phase() RunPhase {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.phase
}

// Snapshot returns a copy of the structure this runtime runs.
func (rt *Runtime) Snapshot() Snapshot { return rt.plan.snapshot.clone() }

// Failed is closed when a module reports a runtime failure (Scope.ReportFailure).
// Err then returns it. The runtime does not stop by itself: its owner decides
// (package app shuts the application down).
func (rt *Runtime) Failed() <-chan struct{} { return rt.fatal }

// Err returns the first runtime failure, or nil.
func (rt *Runtime) Err() error {
	select {
	case <-rt.fatal:
		return rt.fatalErr
	default:
		return nil
	}
}

// Start initializes every enabled module in dependency order and, only after
// all of them succeeded, starts them: the ordinary modules in dependency order,
// then the ingress modules. ctx bounds the start; canceling it (or a
// concurrent Shutdown) aborts it. Background work that must outlive the start
// uses Scope.Context.
//
// On failure everything that was initialized is stopped (ingress first, then
// reverse dependency order) within Options.RollbackTimeout, and the error, a
// *LifecycleError joined with any rollback error, is returned.
func (rt *Runtime) Start(ctx context.Context) error {
	rt.mu.Lock()
	if rt.phase != PhaseValidated {
		phase := rt.phase
		rt.mu.Unlock()
		return fmt.Errorf("%w: start in phase %s", ErrState, phase)
	}
	sctx, cancel := context.WithCancel(ctx)
	rt.phase, rt.startCancel, rt.startDone = PhaseInitializing, cancel, make(chan struct{})
	rt.mu.Unlock()
	defer close(rt.startDone)
	defer cancel()

	err := rt.start(sctx)
	rt.mu.Lock()
	interrupted := rt.phase == PhaseStopping
	if err == nil && !interrupted {
		rt.phase = PhaseStarted
		rt.mu.Unlock()
		return nil
	}
	rt.mu.Unlock()
	if err == nil {
		err = fmt.Errorf("%w: start interrupted by shutdown", context.Canceled)
	}
	if rt.opts.OnFailure != nil {
		rt.opts.OnFailure(err)
	}
	if interrupted {
		return err // Shutdown waits for us and stops what was initialized
	}
	rctx, rcancel := context.WithTimeout(context.WithoutCancel(ctx), rt.opts.RollbackTimeout)
	defer rcancel()
	stopErr := rt.stop(rctx, PhaseFailed)
	return errors.Join(err, stopErr)
}

func (rt *Runtime) start(ctx context.Context) error {
	for _, u := range rt.plan.order {
		if err := rt.interrupted(ctx, u.id, PhaseInit); err != nil {
			return err
		}
		e := rt.entries[u.id]
		if u.def.Init != nil {
			s := &Scope{rt: rt, u: u}
			hooks, err := callInit(ctx, u, s)
			s.sealed.Store(true)
			if err != nil {
				e.state.Store(StateFailed)
				return &LifecycleError{Module: u.id, Phase: PhaseInit, Err: err}
			}
			e.hooks = hooks
		}
		e.initialized = true
		e.state.Store(StateInitialized)
		for _, h := range u.def.Provides {
			rt.mu.Lock()
			_, ok := rt.values[h.ID()]
			rt.mu.Unlock()
			if !ok {
				e.state.Store(StateFailed)
				return &LifecycleError{Module: u.id, Phase: PhaseInit,
					Err: fmt.Errorf("%w: module %q declared %q but did not publish it", ErrNotProvided, u.id, h.ID())}
			}
		}
	}

	rt.mu.Lock()
	if rt.phase == PhaseInitializing {
		rt.phase = PhaseStarting
	}
	rt.mu.Unlock()
	if rt.opts.OnInitialized != nil {
		rt.opts.OnInitialized()
	}
	for _, ingress := range []bool{false, true} {
		for _, u := range rt.plan.order {
			if u.def.Ingress != ingress {
				continue
			}
			if err := rt.interrupted(ctx, u.id, PhaseStart); err != nil {
				return err
			}
			e := rt.entries[u.id]
			if e.hooks.Start != nil {
				if err := call(ctx, "start", e.hooks.Start); err != nil {
					e.state.Store(StateFailed)
					return &LifecycleError{Module: u.id, Phase: PhaseStart, Err: err}
				}
			}
			e.started = true
			e.state.CompareAndSwap(StateInitialized, StateStarted)
		}
	}
	return rt.interrupted(ctx, "", PhaseStart)
}

// interrupted reports a canceled start or a runtime failure reported meanwhile.
func (rt *Runtime) interrupted(ctx context.Context, id ID, phase Phase) error {
	if err := rt.Err(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		if id == "" {
			return fmt.Errorf("start canceled: %w", err)
		}
		return &LifecycleError{Module: id, Phase: phase, Err: fmt.Errorf("start canceled before module %s: %w", id, err)}
	}
	return nil
}

// callInit runs Init and turns a panic (a scope misuse, typically) into an
// error, so a programmer error fails the start and rolls back instead of
// killing the process with resources open.
func callInit(ctx context.Context, u *unit, s *Scope) (hooks Hooks, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("init panicked: %v", r)
		}
	}()
	return u.def.Init(ctx, s)
}

func call(ctx context.Context, name string, fn func(context.Context) error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%s panicked: %v", name, r)
		}
	}()
	return fn(ctx)
}

// Shutdown stops the application once: a start in progress is canceled and
// waited for, then the ingress modules stop (they stop accepting work and
// drain), and only after all of them finished do the other modules stop, each
// one after every module that depends on it. Independent modules stop
// concurrently. Every hook runs even if another fails, and the errors are
// joined. A Stop hook that has not returned when ctx ends plus
// Options.StopGrace is abandoned with ErrStopTimeout, and the modules it
// depends on are left open, because closing them under it could corrupt its
// work. Concurrent and later calls wait for the first one and return its
// result.
func (rt *Runtime) Shutdown(ctx context.Context) error {
	rt.mu.Lock()
	switch rt.phase {
	case PhaseValidated:
		rt.phase = PhaseStopped
		rt.mu.Unlock()
		rt.stopOnce.Do(rt.lifeCancel)
		return nil
	case PhaseInitializing, PhaseStarting:
		rt.phase = PhaseStopping
		rt.startCancel()
	case PhaseStarted:
		rt.phase = PhaseStopping
	}
	done := rt.startDone
	rt.mu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return fmt.Errorf("shutdown: waiting for the start to finish: %w", ctx.Err())
		}
	}
	return rt.stop(ctx, PhaseStopped)
}

func (rt *Runtime) stop(ctx context.Context, final RunPhase) error {
	rt.stopOnce.Do(func() {
		rt.mu.Lock()
		if rt.phase != PhaseFailed {
			rt.phase = PhaseStopping
		}
		rt.mu.Unlock()
		rt.stopErr = rt.stopAll(ctx)
		rt.lifeCancel()
		rt.mu.Lock()
		rt.phase = final
		rt.mu.Unlock()
	})
	return rt.stopErr
}

func (rt *Runtime) stopAll(ctx context.Context) error {
	g := newGrace(ctx, rt.opts.StopGrace)
	defer g.release()
	var ingress, rest []*entry
	for _, u := range rt.plan.order {
		e := rt.entries[u.id]
		if !e.initialized {
			continue
		}
		if u.def.Ingress {
			ingress = append(ingress, e)
		} else {
			rest = append(rest, e)
		}
	}
	var abandoned sync.Map // *unit -> true: stuck, or skipped because a dependent is stuck
	errs := rt.stopSet(ingress, g, &abandoned)
	errs = append(errs, rt.stopSet(rest, g, &abandoned)...)
	return errors.Join(errs...)
}

// stopSet stops set in reverse dependency order, concurrently where the graph
// allows: a module waits for every dependent in the set to finish first.
func (rt *Runtime) stopSet(set []*entry, g *grace, abandoned *sync.Map) []error {
	finished := make(map[*unit]chan struct{}, len(set))
	for _, e := range set {
		finished[e.u] = make(chan struct{})
	}
	var (
		mu   sync.Mutex
		errs []error
		wg   sync.WaitGroup
	)
	record := func(err error) {
		mu.Lock()
		errs = append(errs, err)
		mu.Unlock()
	}
	for _, e := range slices.Backward(set) {
		wg.Go(func() {
			defer close(finished[e.u])
			for _, d := range e.u.dependents {
				if ch, ok := finished[d]; ok {
					<-ch
				}
			}
			for _, d := range e.u.dependents {
				if _, stuck := abandoned.Load(d); stuck {
					abandoned.Store(e.u, true)
					record(&LifecycleError{Module: e.u.id, Phase: PhaseStop,
						Err: fmt.Errorf("%w: module %s left open because %s did not stop", ErrStopTimeout, e.u.id, d.id)})
					return
				}
			}
			e.state.Store(StateStopping)
			if e.hooks.Stop == nil {
				e.state.Store(StateStopped)
				return
			}
			res := make(chan error, 1) // buffered: an abandoned hook must not block forever on send
			go func() { res <- call(g.context(), "stop", e.hooks.Stop) }()
			select {
			case err := <-res:
				e.state.Store(StateStopped)
				if err != nil {
					record(&LifecycleError{Module: e.u.id, Phase: PhaseStop, Err: err})
				}
			case <-g.expired:
				e.state.Store(StateFailed)
				abandoned.Store(e.u, true)
				record(&LifecycleError{Module: e.u.id, Phase: PhaseStop, Err: fmt.Errorf("%w: module %s", ErrStopTimeout, e.u.id)})
			}
		})
	}
	wg.Wait()
	return errs
}

// grace is the single extra window the stop hooks share after the shutdown
// context ended. expired closes at the end of that window.
type grace struct {
	ctx     context.Context
	late    context.Context
	cancel  context.CancelFunc
	expired chan struct{}
	stop    func() bool
	timerMu sync.Mutex
	timer   *time.Timer
}

func newGrace(ctx context.Context, d time.Duration) *grace {
	late, cancel := context.WithCancel(context.WithoutCancel(ctx))
	g := &grace{ctx: ctx, late: late, cancel: cancel, expired: make(chan struct{})}
	g.stop = context.AfterFunc(ctx, func() {
		g.timerMu.Lock()
		defer g.timerMu.Unlock()
		g.timer = time.AfterFunc(d, func() { cancel(); close(g.expired) })
	})
	return g
}

// context is what a hook gets: the shutdown context while it is alive, then
// the grace context.
func (g *grace) context() context.Context {
	if g.ctx.Err() == nil {
		return g.ctx
	}
	return g.late
}

func (g *grace) release() {
	g.stop()
	g.timerMu.Lock()
	defer g.timerMu.Unlock()
	if g.timer != nil {
		g.timer.Stop()
	}
	// The hooks that were abandoned keep g.late; the hooks still running get a
	// canceled context once the shutdown returned.
	g.cancel()
}

// Reload runs the Reload hooks of the started modules in dependency order. A
// failure keeps that module's previous state; the errors are joined.
func (rt *Runtime) Reload(ctx context.Context) error {
	if rt.Phase() != PhaseStarted {
		return fmt.Errorf("%w: reload in phase %s", ErrState, rt.Phase())
	}
	var errs []error
	for _, u := range rt.plan.order {
		e := rt.entries[u.id]
		if !e.started || e.hooks.Reload == nil || e.load() != StateStarted {
			continue
		}
		if err := call(ctx, "reload", e.hooks.Reload); err != nil {
			errs = append(errs, &LifecycleError{Module: u.id, Phase: PhaseReload, Err: err})
		}
	}
	return errors.Join(errs...)
}

func (rt *Runtime) reportFailure(u *unit, err error) {
	if err == nil {
		err = errors.New("unexpected termination")
	}
	switch rt.Phase() {
	case PhaseStopping, PhaseStopped, PhaseFailed:
		return // an ending listener is not a failure
	}
	e := rt.entries[u.id]
	e.state.Store(StateFailed)
	rt.fatalOnce.Do(func() {
		rt.fatalErr = &LifecycleError{Module: u.id, Phase: PhaseRun, Err: err}
		close(rt.fatal)
	})
	rt.invalidateProbe()
}

// Status returns a readiness sample. Samples are shared: a sample younger than
// Options.ProbeInterval is reused, and concurrent callers wait for the probe in
// progress instead of starting their own, so readiness traffic cannot flood
// the dependencies. The probes run with their own Options.ProbeTimeout, not
// with ctx, so a client that disconnects never poisons the shared sample; ctx
// only bounds the caller's wait.
func (rt *Runtime) Status(ctx context.Context) Report {
	rt.probeMu.Lock()
	if r := rt.probeReport; r != nil && time.Since(r.SampledAt) < rt.opts.ProbeInterval && r.Phase == rt.Phase() {
		rt.probeMu.Unlock()
		return r.clone()
	}
	if wait := rt.probing; wait != nil {
		rt.probeMu.Unlock()
		select {
		case <-wait:
			rt.probeMu.Lock()
			defer rt.probeMu.Unlock()
			if rt.probeReport != nil {
				return rt.probeReport.clone()
			}
		case <-ctx.Done():
		}
		return rt.unprobed()
	}
	wait := make(chan struct{})
	rt.probing = wait
	rt.probeMu.Unlock()

	r := rt.probe()
	rt.probeMu.Lock()
	rt.probeReport, rt.probing = &r, nil
	rt.probeMu.Unlock()
	close(wait)
	return r.clone()
}

// invalidateProbe drops the cached sample, so a failure shows at once.
func (rt *Runtime) invalidateProbe() {
	rt.probeMu.Lock()
	rt.probeReport = nil
	rt.probeMu.Unlock()
}

// unprobed reports the states without probing, every enabled module not ready.
func (rt *Runtime) unprobed() Report {
	r := Report{Phase: rt.Phase(), SampledAt: time.Now()}
	for _, m := range rt.plan.snapshot.Modules {
		e := rt.entries[m.ID]
		st := Status{ID: m.ID, State: e.load(), Availability: NotReady, Critical: !e.u.def.NonCritical}
		if st.State == StateDisabled {
			st.Availability = Disabled
		}
		r.Modules = append(r.Modules, st)
	}
	return r
}

func (rt *Runtime) probe() Report {
	ctx, cancel := context.WithTimeout(rt.life, rt.opts.ProbeTimeout)
	defer cancel()
	r := rt.unprobed()
	var wg sync.WaitGroup
	for i := range r.Modules {
		st := &r.Modules[i]
		if st.State != StateStarted {
			continue
		}
		e := rt.entries[st.ID]
		wg.Go(func() { st.Availability = probe(ctx, e.hooks.Ready) })
	}
	wg.Wait()
	r.Ready = r.Phase == PhaseStarted && rt.Err() == nil
	for _, st := range r.Modules {
		// a failure reported during the probe wins over its result
		if st.State != StateDisabled && rt.entries[st.ID].load() == StateFailed {
			st.Availability = NotReady
		}
		if st.Critical && st.Availability == NotReady {
			r.Ready = false
		}
	}
	return r
}

func probe(ctx context.Context, ready func(context.Context) error) Availability {
	if ready == nil {
		return Ready
	}
	done := make(chan error, 1) // buffered: a probe ignoring ctx must not leak forever on send
	go func() { done <- call(ctx, "ready", ready) }()
	select {
	case err := <-done:
		if err != nil {
			return NotReady
		}
		return Ready
	case <-ctx.Done():
		return NotReady
	}
}

func (r Report) clone() Report {
	r.Modules = slices.Clone(r.Modules)
	return r
}

// Value returns the published value of c, for code outside the module graph
// that holds the Runtime (the application's main). ok is false when nothing
// was published under c or when it holds another type.
func Value[T any](rt *Runtime, c Capability[T]) (T, bool) {
	rt.mu.Lock()
	v, ok := rt.values[c.id]
	rt.mu.Unlock()
	t, isT := v.(T)
	return t, ok && isT
}

// Introspection is the read-only view of the running application that a
// module may hold (the HTTP readiness route and the graph page).
type Introspection interface {
	Status(ctx context.Context) Report
	Snapshot() Snapshot
	Phase() RunPhase
}

// Scope is what a module sees during Init: only the capabilities it declared.
// That keeps it from becoming a service locator: the graph always shows the
// real dependencies. It is dependency discipline, not a sandbox: a shared
// resource enforces no authorization of its own. Misuse is a programmer error
// and panics; the runtime turns the panic into an Init error.
type Scope struct {
	rt     *Runtime
	u      *unit
	sealed atomic.Bool
}

// ID returns the module's ID.
func (s *Scope) ID() ID { return s.u.id }

// App returns the application metadata.
func (s *Scope) App() App { return s.u.src.App() }

// ProcessGlobals reports whether the application installs process defaults.
func (s *Scope) ProcessGlobals() bool { return s.u.src.ProcessGlobals() }

// Context lives until the application has shut down. Background work of the
// module may derive from it; the module still stops that work in its Stop hook.
func (s *Scope) Context() context.Context { return s.rt.life }

// ReportFailure reports that the module failed after it started (a listener
// that died, a consumer that cannot reconnect). The module is marked failed,
// readiness drops at once and the application owner is notified
// (Runtime.Failed). Recoverable outages are not failures: they make Ready
// return an error until they end. Reports during shutdown are ignored.
func (s *Scope) ReportFailure(err error) { s.rt.reportFailure(s.u, err) }

// Introspect returns the read-only view of the application.
func (s *Scope) Introspect() Introspection { return s.rt }

// Provide publishes v as c. c must be in Definition.Provides with this type,
// and Init must still be running.
func Provide[T any](s *Scope, c Capability[T], v T) {
	if s.sealed.Load() {
		panic(fmt.Sprintf("module: %s published %q after Init returned", s.u.id, c.id))
	}
	i := slices.IndexFunc(s.u.def.Provides, func(h Handle) bool { return h.ID() == c.id })
	if i < 0 {
		panic(fmt.Sprintf("module: %s published %q, which it does not declare in Provides", s.u.id, c.id))
	}
	if declared := s.u.def.Provides[i]; declared.valueType() != c.valueType() {
		panic(fmt.Sprintf("module: %s published %q as %v, but declared it as %v", s.u.id, c.id, c.valueType(), declared.valueType()))
	}
	s.rt.mu.Lock()
	defer s.rt.mu.Unlock()
	s.rt.values[c.id] = v
}

// Require returns a capability from Definition.Requires. Build and the start
// order guarantee that it exists.
func Require[T any](s *Scope, c Capability[T]) T {
	v, ok := Lookup(s, c)
	if !ok {
		panic(fmt.Sprintf("module: %s requires %q, which was not published", s.u.id, c.id))
	}
	return v
}

// Lookup returns a capability declared in Definition.Requires or Optional and
// whether its provider is enabled. An undeclared capability, or a handle of
// another type than the declared one, panics.
func Lookup[T any](s *Scope, c Capability[T]) (T, bool) {
	declared, ok := s.u.declared[c.id]
	if !ok {
		panic(fmt.Sprintf("module: %s read %q without declaring it", s.u.id, c.id))
	}
	if declared.valueType() != c.valueType() {
		panic(fmt.Sprintf("module: %s read %q as %v, but declared it as %v", s.u.id, c.id, c.valueType(), declared.valueType()))
	}
	return Value(s.rt, c)
}
