package deps

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/ams-soft/nforge/cache"
	"github.com/ams-soft/nforge/codec"
	"github.com/ams-soft/nforge/config"
	"github.com/ams-soft/nforge/database"
	"github.com/ams-soft/nforge/jose"
	"github.com/ams-soft/nforge/module"
)

// Deps holds what the modules built. Use it through a pointer: it is shared by
// the server and the application and must not be copied.
type Deps struct {
	Context context.Context
	Config  config.Config
	// Logger is never nil in a Deps built by Bootstrap: it discards everything
	// when logging is off.
	Logger *slog.Logger
	Codec  *codec.Registry
	// DB is nil when database.enabled is false.
	DB *database.DB
	// JOSE is nil when jose.enabled is false.
	JOSE *jose.Service
	// Cache is nil when cache.enabled is false.
	Cache *cache.Cache

	runtime *module.Runtime // nil for a Deps not built by Bootstrap

	mu      sync.Mutex // guards closers: Shutdown may be called from two places
	closers []func(context.Context) error
}

// ErrDatabaseDisabled is returned by PingDatabase when the database was not
// enabled in the config.
var ErrDatabaseDisabled = errors.New("deps: database not enabled (database.enabled: false)")

// ErrJOSEDisabled is returned by CheckJOSE when jose.enabled is false.
var ErrJOSEDisabled = errors.New("deps: jose not enabled (jose.enabled: false)")

// ErrCacheDisabled is returned by PingCache when cache.enabled is false.
var ErrCacheDisabled = errors.New("deps: cache not enabled (cache.enabled: false)")

const abortShutdownTimeout = 5 * time.Second

// AddShutdown registers fn to run in Shutdown. Functions run last in, first out,
// and every one registered after Bootstrap runs before the modules stop, so it
// can still use them.
func (d *Deps) AddShutdown(fn func(context.Context) error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.closers = append(d.closers, fn)
}

// Shutdown runs the registered shutdown functions in reverse order (LIFO,
// like defer): what was initialized last closes first. This way the database
// closes before logging/tracing, which still log their own shutdown. Bootstrap
// registers one function, the module runtime, whose order is the reverse of the
// start order: cache, JOSE, database, tracing, logs.
// All of them run even if one fails; the errors are joined.
func (d *Deps) Shutdown(ctx context.Context) error {
	// Take the functions out under the lock, so a second (concurrent or later)
	// call finds nothing and no closer runs twice.
	d.mu.Lock()
	fns := d.closers
	d.closers = nil
	d.mu.Unlock()

	var errs []error
	for i := len(fns) - 1; i >= 0; i-- {
		if err := fns[i](ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// PingDatabase checks the PostgreSQL connection, for readiness/health
// checks. Use a ctx with a short timeout (e.g. 2s).
func (d *Deps) PingDatabase(ctx context.Context) error {
	if d.DB == nil {
		return ErrDatabaseDisabled
	}
	return d.DB.Ping(ctx)
}

// CheckJOSE reports whether the JOSE service admits operations, for
// readiness checks. It does no I/O and no cryptography.
func (d *Deps) CheckJOSE(ctx context.Context) error {
	if d.JOSE == nil {
		return ErrJOSEDisabled
	}
	return d.JOSE.Check(ctx)
}

// ReloadJOSE re-reads the key files of the running JOSE service (key rotation
// without a restart; see jose.Service.Reload). nhttp calls it on SIGHUP.
func (d *Deps) ReloadJOSE(ctx context.Context) error {
	if d.JOSE == nil {
		return ErrJOSEDisabled
	}
	return d.JOSE.Reload(ctx)
}

// PingCache checks the cache backend (Redis PING; memory: open), for
// readiness checks. Use a ctx with a short timeout (e.g. 2s).
func (d *Deps) PingCache(ctx context.Context) error {
	if d.Cache == nil {
		return ErrCacheDisabled
	}
	return d.Cache.Ping(ctx)
}

// NewDeps builds the dependencies of cfg and exits the process with status 1
// when a required module cannot start (see NewDepsE). The reason is always
// written to stderr, whatever the logging configuration, because with logging
// disabled (the default) the logger discards everything.
func NewDeps(cfg config.Config) *Deps {
	d, err := NewDepsE(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[Fatal ERROR] %v\n", err)
		os.Exit(1)
	}
	return d
}

// NewDepsE is NewDeps that returns the startup error instead of exiting, for
// embedding and tests. See Bootstrap, which it calls with no extra modules.
func NewDepsE(cfg config.Config) (*Deps, error) {
	return Bootstrap(context.Background(), cfg)
}

// Bootstrap resolves the module graph of cfg and starts it. The built-in
// modules (modules.go) come first, then extra, which may depend on them through
// the exported capabilities.
//
// The graph is validated before anything starts: a required module that is
// disabled or missing, a cycle or an ambiguous provider fails here, with nothing
// opened and nothing enabled on the caller's behalf. A failure while starting
// logs the error through the logger built so far, shuts down what had started
// (so buffered logs and traces are flushed) and returns the error joined with
// any shutdown error; the returned Deps is nil.
//
// Canceling ctx aborts a start in progress and has no effect after Bootstrap
// returns; Deps.Shutdown is the way to stop.
func Bootstrap(ctx context.Context, cfg config.Config, extra ...Module) (*Deps, error) {
	plan, err := module.Resolve(cfg, append(builtinModules(), extra...))
	if err != nil {
		return nil, fmt.Errorf("unable to resolve modules: %w", err)
	}

	d := &Deps{Config: cfg, Context: context.WithoutCancel(ctx)}
	// The modules outlive ctx: only Runtime.Shutdown ends them, not the caller.
	startCtx, stopWatching := detachAfterStart(ctx)
	defer stopWatching()
	rt, err := plan.Start(startCtx, module.StartOptions{
		RollbackTimeout: abortShutdownTimeout,
		OnFailure: func(rt *module.Runtime, cause error) {
			if logger, ok := module.Value(rt, CapLogger); ok {
				logger.ErrorContext(d.Context, "[Fatal ERROR] "+cause.Error())
			}
		},
	})
	if err != nil {
		return nil, err
	}

	d.runtime = rt
	d.AddShutdown(rt.Shutdown) // one closer: the runtime is the only sequencer
	d.Logger, _ = module.Value(rt, CapLogger)
	d.Codec, _ = module.Value(rt, CapCodec)
	d.DB, _ = module.Value(rt, CapDatabase)
	d.JOSE, _ = module.Value(rt, CapJOSE)
	d.Cache, _ = module.Value(rt, CapCache)
	return d, nil
}

// detachAfterStart returns a context that follows ctx until stop is called and
// is then only ended by its owner, because Plan.Start keeps the context it gets
// alive until shutdown while a Bootstrap caller's ctx may end right after it.
func detachAfterStart(ctx context.Context) (context.Context, func()) {
	detached, cancel := context.WithCancel(context.WithoutCancel(ctx))
	if ctx.Err() != nil {
		cancel() // AfterFunc runs in its own goroutine, possibly after the start
	}
	stop := context.AfterFunc(ctx, cancel)
	return detached, func() { stop() }
}

// Graph returns the structure resolved at bootstrap. ok is false for a Deps
// that Bootstrap did not build. The structure never changes after bootstrap.
func (d *Deps) Graph() (graph module.Snapshot, ok bool) {
	if d.runtime == nil {
		return module.Snapshot{}, false
	}
	return d.runtime.Snapshot(), true
}

// ModuleStatus probes the modules; see module.Runtime.Status. It returns nil
// for a Deps that Bootstrap did not build. Use a ctx with a short timeout.
func (d *Deps) ModuleStatus(ctx context.Context) []module.Status {
	if d.runtime == nil {
		return nil
	}
	return d.runtime.Status(ctx)
}

// Value returns the resource published under c, for capabilities of modules
// passed to Bootstrap; the built-in ones have their own Deps fields.
func Value[T any](d *Deps, c module.Capability[T]) (T, bool) {
	if d.runtime == nil {
		var zero T
		return zero, false
	}
	return module.Value(d.runtime, c)
}
