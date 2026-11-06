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
	// defaultRollbackTimeout bounds the rollback of a failed start.
	defaultRollbackTimeout = 5 * time.Second
	// shutdownGrace is what each remaining hook gets once the Shutdown context has
	// ended. Providers stop last, so without it a consumer that hangs until the
	// deadline would cost the telemetry flush that explains why it hung.
	shutdownGrace = time.Second
)

// Initialization is where a module is in its lifecycle.
type Initialization string

// Lifecycle states.
const (
	StatePending  Initialization = "pending"
	StateStarted  Initialization = "started"
	StateFailed   Initialization = "failed"
	StateStopped  Initialization = "stopped"
	StateDisabled Initialization = "disabled"
)

// Availability is the result of probing a module now.
type Availability string

// Availability values. A disabled module is not an unavailable one.
const (
	Ready    Availability = "ready"
	NotReady Availability = "not_ready"
	Disabled Availability = "disabled"
)

// Status is the operational state of one module. It lives apart from the
// immutable Snapshot: it changes, the structure does not.
type Status struct {
	ID             ID
	Initialization Initialization
	Availability   Availability
}

// StartOptions tunes Plan.Start. The zero value is valid.
type StartOptions struct {
	// RollbackTimeout bounds the rollback after a failure; 0 means 5 s. It is
	// independent of the start context, which may already be canceled.
	RollbackTimeout time.Duration
	// OnFailure runs before the rollback, while the modules that started are
	// still usable (a logger, typically). It receives the failure cause.
	OnFailure func(rt *Runtime, cause error)
}

// Runtime is a started plan: the published resources, the hooks and the
// operational state. All its methods are safe for concurrent use.
type Runtime struct {
	snapshot Snapshot
	cancel   context.CancelFunc

	mu      sync.RWMutex
	values  map[CapabilityID]any
	entries map[ID]*entry
	started []*entry // start order; emptied by Shutdown, so it runs once
}

type entry struct {
	id    ID
	hooks Hooks
	state atomic.Value // Initialization
}

// Start initializes the enabled modules in plan order. ctx reaches every Init
// and stays alive until Shutdown, so a module may tie background work to it;
// canceling it during the start aborts the start.
//
// On failure the pending work is canceled, the modules already started are shut
// down in reverse order, and the error (an *InitError joined with any rollback
// error) is returned along with the stopped Runtime.
func (p *Plan[C]) Start(ctx context.Context, opts StartOptions) (*Runtime, error) {
	ctx, cancel := context.WithCancel(ctx)
	rt := &Runtime{
		snapshot: p.snapshot.clone(),
		cancel:   cancel,
		values:   map[CapabilityID]any{},
		entries:  make(map[ID]*entry, len(p.snapshot.Modules)),
	}
	for _, m := range p.snapshot.Modules {
		e := &entry{id: m.ID}
		e.state.Store(StateDisabled)
		if m.Enabled {
			e.state.Store(StatePending)
		}
		rt.entries[m.ID] = e
	}
	for _, m := range p.modules {
		if err := p.startOne(ctx, rt, m); err != nil {
			rt.entries[m.ID].state.CompareAndSwap(StatePending, StateFailed)
			cause := &InitError{Module: m.ID, Err: err}
			return rt, rt.rollback(ctx, cause, opts)
		}
	}
	return rt, nil
}

func (p *Plan[C]) startOne(ctx context.Context, rt *Runtime, m Module[C]) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("start canceled before module %s: %w", m.ID, err)
	}
	e := rt.entries[m.ID]
	var hooks Hooks
	if m.Init != nil {
		s := &Scope{rt: rt, id: m.ID, reads: p.reads[m.ID], provides: m.Provides}
		var err error
		hooks, err = m.Init(ctx, p.cfg, s)
		s.sealed.Store(true)
		if err != nil {
			return err
		}
	}
	e.hooks = hooks
	e.state.Store(StateStarted)
	rt.mu.Lock()
	rt.started = append(rt.started, e)
	rt.mu.Unlock()
	if m.Init == nil {
		return nil
	}
	for _, c := range m.Provides {
		rt.mu.RLock()
		_, ok := rt.values[c]
		rt.mu.RUnlock()
		if !ok {
			return fmt.Errorf("%w: module %q declared %q but did not publish it", ErrNotProvided, m.ID, c)
		}
	}
	return nil
}

func (rt *Runtime) rollback(ctx context.Context, cause error, opts StartOptions) error {
	if opts.OnFailure != nil {
		opts.OnFailure(rt, cause)
	}
	timeout := opts.RollbackTimeout
	if timeout <= 0 {
		timeout = defaultRollbackTimeout
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	return errors.Join(cause, rt.Shutdown(rctx))
}

// Shutdown stops the started modules in reverse start order, so consumers stop
// before their providers. Every hook runs even if another fails, and a hook that
// starts after ctx ended still gets a short bounded context of its own, so the
// call can outlast ctx by one second per remaining hook. The errors are joined.
// Only the first call does anything.
func (rt *Runtime) Shutdown(ctx context.Context) error {
	rt.mu.Lock()
	started := rt.started
	rt.started = nil
	rt.mu.Unlock()

	var errs []error
	for _, e := range slices.Backward(started) {
		if e.hooks.Shutdown != nil {
			if err := runShutdown(ctx, e.hooks.Shutdown); err != nil {
				errs = append(errs, err)
			}
		}
		e.state.Store(StateStopped)
	}
	rt.cancel()
	return errors.Join(errs...)
}

func runShutdown(ctx context.Context, hook func(context.Context) error) error {
	if ctx.Err() == nil {
		return hook(ctx)
	}
	grace, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownGrace)
	defer cancel()
	return hook(grace)
}

// Snapshot returns a copy of the structure this runtime was started from.
func (rt *Runtime) Snapshot() Snapshot { return rt.snapshot.clone() }

// Status probes every started module concurrently and reports all of them,
// sorted by ID. A probe that outlives ctx counts as not ready. Failure details
// are not reported here: they stay with the caller's own logging of the probe.
func (rt *Runtime) Status(ctx context.Context) []Status {
	out := make([]Status, len(rt.snapshot.Modules))
	var wg sync.WaitGroup
	for i, m := range rt.snapshot.Modules {
		e := rt.entries[m.ID]
		state, _ := e.state.Load().(Initialization)
		out[i] = Status{ID: m.ID, Initialization: state, Availability: NotReady}
		switch state {
		case StateDisabled:
			out[i].Availability = Disabled
		case StateStarted:
			wg.Go(func() { out[i].Availability = probe(ctx, e.hooks.Ready) })
		}
	}
	wg.Wait()
	return out
}

func probe(ctx context.Context, ready func(context.Context) error) Availability {
	if ready == nil {
		return Ready
	}
	done := make(chan error, 1) // buffered: a probe ignoring ctx must not leak forever on send
	go func() { done <- ready(ctx) }()
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

// Value returns the published value of c, for code outside the module graph
// that already holds the Runtime.
func Value[T any](rt *Runtime, c Capability[T]) (T, bool) {
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	v, ok := rt.values[c.id]
	if !ok {
		var zero T
		return zero, false
	}
	return mustCast[T](v, c.id), true
}

func mustCast[T any](v any, id CapabilityID) T {
	t, ok := v.(T)
	if !ok {
		panic(fmt.Sprintf("module: capability %q holds %T, not the requested type", id, v))
	}
	return t
}

// Scope is what a module sees during Init: only the capabilities it declared.
// That keeps it from becoming a service locator, as the graph could then no
// longer describe the real dependencies. Misuse is a programmer error and panics.
type Scope struct {
	rt       *Runtime
	id       ID
	reads    map[CapabilityID]bool
	provides []CapabilityID
	sealed   atomic.Bool
}

// Provide publishes v as c. c must be in Module.Provides and Init still running.
func Provide[T any](s *Scope, c Capability[T], v T) {
	if s.sealed.Load() {
		panic(fmt.Sprintf("module: %s published %q after Init returned", s.id, c.id))
	}
	if !slices.Contains(s.provides, c.id) {
		panic(fmt.Sprintf("module: %s published %q, which it does not declare in Provides", s.id, c.id))
	}
	s.rt.mu.Lock()
	defer s.rt.mu.Unlock()
	s.rt.values[c.id] = v
}

// Require returns a capability from Module.Requires. Resolve and the start order
// guarantee that it exists.
func Require[T any](s *Scope, c Capability[T]) T {
	v, ok := Lookup(s, c)
	if !ok {
		panic(fmt.Sprintf("module: %s requires %q, which was not published", s.id, c.id))
	}
	return v
}

// Lookup returns a capability declared in Module.Requires or Module.Optional and
// whether its provider is enabled. An undeclared capability panics, even when
// another module publishes it.
func Lookup[T any](s *Scope, c Capability[T]) (T, bool) {
	if !s.reads[c.id] {
		// An optional capability without an enabled provider has no edge, so only
		// a read of something published proves the declaration is missing.
		if _, published := Value(s.rt, c); published {
			panic(fmt.Sprintf("module: %s read %q without declaring it", s.id, c.id))
		}
		var zero T
		return zero, false
	}
	return Value(s.rt, c)
}
