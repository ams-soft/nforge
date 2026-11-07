// Package app is the application-level orchestrator of nForge: it registers
// modules explicitly, reads the configuration once, resolves and validates the
// module graph, runs the coordinated Init/Start, aggregates readiness, handles
// runtime failures and shuts down once, gracefully. It depends only on module
// and config: the database, HTTP, JOSE, cache, logging and telemetry modules
// are registered by the application, never imported here, so an application
// can be HTTP-only, worker-only, both, or anything else.
//
// The library API returns errors and never exits the process; Run adds the
// process-level signal handling for a main package, which alone chooses the
// exit code.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ams-soft/nforge/config"
	"github.com/ams-soft/nforge/module"
)

const defaultShutdownTimeout = 15 * time.Second

// loggerCapability is where a logging module publishes the application
// logger. The app knows only the capability, never the logging module.
var loggerCapability = module.Logger

// Option configures New.
type Option func(*options)

type options struct {
	modules         []module.Descriptor
	logger          *slog.Logger
	globals         bool
	shutdownTimeout time.Duration
	runtime         module.Options
}

// Modules registers modules. Registration is explicit and authoritative: the
// configuration can configure or disable a registered module, never add one.
// The registration order breaks ties in the start order.
func Modules(mods ...module.Descriptor) Option {
	return func(o *options) { o.modules = append(o.modules, mods...) }
}

// WithLogger sets the bootstrap logger, used until a module publishes the
// "logger" capability and again after it stopped. The default writes WARN and
// above to stderr, so a fatal startup error is visible whatever the logging
// configuration.
func WithLogger(l *slog.Logger) Option { return func(o *options) { o.logger = l } }

// WithProcessGlobals lets the modules install process-wide defaults
// (slog.Default, codec.Default, the OTel provider, propagator and error
// handler), for applications and libraries that read them. Without it nothing
// global is touched, so several applications can run in one process.
func WithProcessGlobals() Option { return func(o *options) { o.globals = true } }

// WithShutdownTimeout bounds the shutdown that Run starts; default 15 s.
func WithShutdownTimeout(d time.Duration) Option {
	return func(o *options) { o.shutdownTimeout = d }
}

// WithProbeInterval sets how long a readiness sample is reused (default 1 s)
// and the timeout of one sample (default 2 s).
func WithProbeInterval(interval, timeout time.Duration) Option {
	return func(o *options) { o.runtime.ProbeInterval, o.runtime.ProbeTimeout = interval, timeout }
}

// Application is one nForge application. Use it through a pointer; all its
// methods are safe for concurrent use.
type Application struct {
	doc      *config.Document
	plan     *module.Plan
	rt       *module.Runtime
	opts     options
	warnings []string

	warnOnce sync.Once
}

// New registers the modules, decodes and validates every configuration section
// and resolves the graph, with no I/O: duplicate IDs, sections no module owns,
// unknown keys, invalid values, ambiguous or missing providers, capability type
// mismatches and cycles all fail here, aggregated, before anything is opened.
func New(cfg *config.Document, opts ...Option) (*Application, error) {
	if cfg == nil {
		return nil, errors.New("app: nil configuration")
	}
	o := options{shutdownTimeout: defaultShutdownTimeout}
	for _, opt := range opts {
		opt(&o)
	}
	if o.logger == nil {
		o.logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	}
	if o.shutdownTimeout <= 0 {
		o.shutdownTimeout = defaultShutdownTimeout
	}
	a := &Application{doc: cfg, opts: o}
	binding := cfg.Bind()
	policy := &policy{env: cfg.Env(), exceptions: cfg.PrdExceptions()}
	meta := module.App{
		Name: cfg.App().Name, Version: cfg.App().Version, Env: cfg.Env(),
		InternalUse: cfg.App().InternalUse, OnlineSince: cfg.OnlineSince(),
	}
	for _, d := range o.modules {
		if d.Section != "" {
			binding.Own(d.Section)
		}
	}
	plan, err := module.Build(o.modules, func(d module.Descriptor) module.Source {
		return &source{binding: binding, section: d.Section, app: meta, globals: o.globals, policy: policy}
	})
	errs := []error{}
	if err != nil {
		errs = append(errs, err)
	}
	if err := binding.Finish(); err != nil {
		errs = append(errs, err)
	}
	if err := policy.err(); err != nil {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("app: invalid application: %w", errors.Join(errs...))
	}
	a.plan, a.warnings = plan, policy.warnings
	o.runtime.OnFailure = a.onFailure
	o.runtime.OnInitialized = func() { a.logWarnings(context.Background()) }
	a.rt = plan.NewRuntime(o.runtime)
	return a, nil
}

// Start initializes and starts every module, or rolls back completely: on
// error nothing stays open. It does not handle signals and does not block once
// the modules are active. ctx bounds the start only.
func (a *Application) Start(ctx context.Context) error {
	if err := a.rt.Start(ctx); err != nil {
		return fmt.Errorf("app: start: %w", err)
	}
	a.logger().InfoContext(ctx, "[App] Application started", "modules", len(a.plan.Snapshot().Order))
	return nil
}

// Run starts the application and blocks until ctx ends, SIGINT or SIGTERM
// arrives, or a module reports a runtime failure; it then shuts down within
// the shutdown timeout and returns the failure joined with any shutdown error
// (nil after a clean signal-driven stop). SIGHUP reloads the modules'
// credentials (certificates, key files) without a restart. Run works with any
// set of modules, HTTP or not.
func (a *Application) Run(ctx context.Context) error {
	sigctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)

	if err := a.Start(sigctx); err != nil {
		return err
	}
	var cause error
loop:
	for {
		select {
		case <-sigctx.Done():
			a.logger().InfoContext(ctx, "[App] Shutting down")
			break loop
		case <-a.rt.Failed():
			cause = fmt.Errorf("app: %w", a.rt.Err())
			a.logger().ErrorContext(ctx, "[Fatal ERROR] "+a.rt.Err().Error())
			break loop
		case <-hup:
			if err := a.Reload(sigctx); err != nil {
				a.logger().WarnContext(ctx, "[App] Reload failed; the previous credentials stay in use", "error", err)
			} else {
				a.logger().InfoContext(ctx, "[App] Credentials reloaded")
			}
		}
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), a.opts.shutdownTimeout)
	defer cancel()
	return errors.Join(cause, a.Shutdown(sctx))
}

// Shutdown stops the application once (see module.Runtime.Shutdown): a start
// in progress is canceled, the ingress modules stop accepting and drain, then
// the rest closes in reverse dependency order. ctx is the global deadline.
// It is idempotent and safe for concurrent use; every call returns the
// aggregated result of the first.
func (a *Application) Shutdown(ctx context.Context) error {
	if err := a.rt.Shutdown(ctx); err != nil {
		// the bootstrap logger: the configured one may have stopped
		a.opts.logger.ErrorContext(ctx, "[App] Shutdown finished with errors", "error", err)
		return fmt.Errorf("app: shutdown: %w", err)
	}
	return nil
}

// Reload runs the modules' Reload hooks (credential rotation). A failing
// module keeps its previous credentials.
func (a *Application) Reload(ctx context.Context) error { return a.rt.Reload(ctx) }

// Status returns the phase, the aggregate readiness and every module's state,
// independently of any transport. Probes are bounded and shared between
// concurrent callers; see WithProbeInterval.
func (a *Application) Status(ctx context.Context) module.Report { return a.rt.Status(ctx) }

// Failed is closed when a module reports a runtime failure; Err returns it.
func (a *Application) Failed() <-chan struct{} { return a.rt.Failed() }

// Err returns the first runtime failure reported by a module, or nil.
func (a *Application) Err() error { return a.rt.Err() }

// Graph returns the resolved module graph. It never changes.
func (a *Application) Graph() module.Snapshot { return a.plan.Snapshot() }

// Warnings returns the configuration warnings: valid settings that weaken the
// deployment.
func (a *Application) Warnings() []string { return slices.Clone(a.warnings) }

// Config returns the configuration the application was built from.
func (a *Application) Config() *config.Document { return a.doc }

// Value returns the resource published under c, for the main package or tests
// that need a module's resource from outside the graph. Module code declares
// its dependencies and uses module.Require instead.
func Value[T any](a *Application, c module.Capability[T]) (T, bool) { return module.Value(a.rt, c) }

// logger is the application logger once published, else the bootstrap one.
func (a *Application) logger() *slog.Logger {
	if l, ok := module.Value(a.rt, loggerCapability); ok && l != nil {
		return l
	}
	return a.opts.logger
}

// logWarnings reports the configuration warnings once. env prd filters the log
// below ERROR, so there they are logged at ERROR: the operator must see them.
func (a *Application) logWarnings(ctx context.Context) {
	a.warnOnce.Do(func() {
		level := slog.LevelWarn
		if a.doc.Env() == "prd" {
			level = slog.LevelError
		}
		for _, w := range a.warnings {
			a.logger().Log(ctx, level, "[Config] "+w)
		}
	})
}

func (a *Application) onFailure(cause error) {
	a.logWarnings(context.Background())
	a.logger().Error("[Fatal ERROR] " + cause.Error())
}

// source is the module.Source of one module.
type source struct {
	binding *config.Binding
	section string
	app     module.App
	globals bool
	policy  *policy
}

func (s *source) Decode(dst any) error {
	if s.section == "" {
		return errors.New("app: the module declares no configuration section")
	}
	return s.binding.Decode(s.section, dst)
}

func (s *source) Present() bool        { return s.section != "" && s.binding.Present(s.section) }
func (s *source) App() module.App      { return s.app }
func (s *source) ProcessGlobals() bool { return s.globals }
func (s *source) Warn(exception, msg string) {
	s.policy.report(exception, msg)
}

// policy turns weakening settings into warnings, or into errors in prd unless
// prd_exceptions accepts them.
type policy struct {
	env        string
	exceptions config.PrdExceptions
	mu         sync.Mutex
	warnings   []string
	refused    []string
}

func (p *policy) report(exception, msg string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if exception != "" && p.env == "prd" && !p.exceptions.Allows(exception) {
		p.refused = append(p.refused, fmt.Sprintf("%s (accept it with prd_exceptions.%s: true)", msg, exception))
		return
	}
	p.warnings = append(p.warnings, msg)
}

func (p *policy) err() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.refused) == 0 {
		return nil
	}
	return fmt.Errorf("env prd refuses insecure settings:\n  - %s", strings.Join(p.refused, "\n  - "))
}
