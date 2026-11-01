package deps

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/ams-soft/nforge"
	"github.com/ams-soft/nforge/cache"
	"github.com/ams-soft/nforge/codec"
	"github.com/ams-soft/nforge/config"
	"github.com/ams-soft/nforge/database"
	"github.com/ams-soft/nforge/jose"
	"github.com/ams-soft/nforge/logs"
	"github.com/ams-soft/nforge/telemetry"
)

type Aux struct {
	ShutdownFuncs []func(context.Context) error
}
type Deps struct {
	Context context.Context
	Config  config.Config
	Logger  slog.Logger
	Codec   *codec.Registry
	// DB is nil when database.enabled is false.
	DB *database.DB
	// JOSE is nil when jose.enabled is false.
	JOSE *jose.Service
	// Cache is nil when cache.enabled is false.
	Cache *cache.Cache
	Aux   *Aux
}

// ErrDatabaseDisabled is returned by PingDatabase when the database was not
// enabled in the config.
var ErrDatabaseDisabled = errors.New("deps: database not enabled (database.enabled: false)")

// ErrJOSEDisabled is returned by CheckJOSE when jose.enabled is false.
var ErrJOSEDisabled = errors.New("deps: jose not enabled (jose.enabled: false)")

// ErrCacheDisabled is returned by PingCache when cache.enabled is false.
var ErrCacheDisabled = errors.New("deps: cache not enabled (cache.enabled: false)")

const (
	databaseStartupTimeout = 15 * time.Second
	joseStartupTimeout     = 15 * time.Second
	cacheStartupTimeout    = 15 * time.Second
	abortShutdownTimeout   = 5 * time.Second
)

func (d *Deps) addShutdownFunc(fn func(context.Context) error) {
	d.Aux.ShutdownFuncs = append(d.Aux.ShutdownFuncs, fn)
}

// Shutdown runs the registered shutdown functions in reverse order (LIFO,
// like defer): what was initialized last closes first. This way the database
// closes before logging/tracing, which still log their own shutdown. The
// resulting order is cache, JOSE, database, tracing, logs.
// All of them run even if one fails; the errors are joined.
func (d *Deps) Shutdown(ctx context.Context) error {
	if d.Aux == nil {
		return nil
	}
	var errs []error
	fns := d.Aux.ShutdownFuncs
	for i := len(fns) - 1; i >= 0; i-- {
		if err := fns[i](ctx); err != nil {
			errs = append(errs, err)
		}
	}
	d.Aux.ShutdownFuncs = nil
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

// PingCache checks the cache backend (Redis PING; memory: open), for
// readiness checks. Use a ctx with a short timeout (e.g. 2s).
func (d *Deps) PingCache(ctx context.Context) error {
	if d.Cache == nil {
		return ErrCacheDisabled
	}
	return d.Cache.Ping(ctx)
}

func (d *Deps) ClearAux() {
	d.Aux = nil
}
func (d *Deps) setupLogger(ctx context.Context, config config.Config) error {
	if !config.IsLoggingEnabled() {
		d.Logger = *logs.NewNoop()
		return nil
	}
	logger, shutdown, err := logs.Setup(ctx, config)
	if err != nil {
		d.Logger = *logs.NewNoop() // abort still logs; a zero slog.Logger would panic
		return fmt.Errorf("unable to setup logger: %w", err)
	}
	d.addShutdownFunc(shutdown)

	if config.IsTracingEnabled() {
		shutdownTracing, err := telemetry.SetupTracing(ctx, config)
		if err != nil {
			d.Logger = *logger // so abort can still report the failure
			return fmt.Errorf("unable to setup tracing: %w", err)
		}
		d.addShutdownFunc(shutdownTracing)
	}

	d.Logger = *logger
	return nil
}

// setupCodec builds the Registry from the config and installs it as the
// process default, which is the one used by the http helpers (SendJSON, ParseXML...).
func (d *Deps) setupCodec(cfg config.Config) error {
	registry, err := codec.New(codec.Config{
		Formats:             cfg.Codec.Formats.Enabled,
		Default:             cfg.Codec.Default,
		StrictUnknownFields: cfg.Codec.StrictUnknownFields,
		MaxPayloadBytes:     int64(cfg.Codec.MaxPayloadBytes),
		AllowDTD:            cfg.Codec.XML.DisallowDTD != nil && !*cfg.Codec.XML.DisallowDTD,
	})
	if err != nil {
		return fmt.Errorf("unable to setup codec: %w", err)
	}
	codec.SetDefault(registry)
	d.Codec = registry
	d.Logger.InfoContext(d.Context, "  [CodecModule] Codec setup completed")
	return nil
}

// setupDatabase opens the pool (with a startup ping) when database.enabled is
// true. A connection failure is fatal: the application does not start without the database.
func (d *Deps) setupDatabase(cfg config.Config) error {
	if !cfg.Database.Enabled {
		return nil
	}
	ctx, cancel := context.WithTimeout(d.Context, databaseStartupTimeout)
	defer cancel()

	db, err := database.Open(ctx, cfg.Database)
	if err != nil {
		return fmt.Errorf("unable to setup database: %w", err)
	}
	d.DB = db
	d.addShutdownFunc(db.Shutdown)
	d.Logger.InfoContext(d.Context, "[DatabaseModule] Database setup completed")
	return nil
}

// setupJOSE loads the JOSE keys when jose.enabled is true. A failure is
// fatal: the application does not start with broken key material. The error
// is already sanitized by the jose package (no paths or key bytes).
func (d *Deps) setupJOSE(cfg config.Config) error {
	if !cfg.JOSE.Enabled {
		return nil
	}
	ctx, cancel := context.WithTimeout(d.Context, joseStartupTimeout)
	defer cancel()

	service, err := jose.New(ctx, cfg.JOSE)
	if err != nil {
		return fmt.Errorf("unable to setup jose: %w", err)
	}
	d.JOSE = service
	d.addShutdownFunc(service.Shutdown)
	d.Logger.InfoContext(d.Context, "[JOSEModule] JOSE setup completed", "jose", cfg.JOSE)
	return nil
}

// setupCache opens the cache when cache.enabled is true, after the codec
// (objects use d.Codec) and the database (so LIFO closes the cache first).
// With database.tenancy.enabled the keys are isolated per tenant, read from
// the same context value as the database (database.TenantFromContext, set
// by the nhttp tenant middlewares). A startup failure (e.g. Redis down) is
// fatal, like the database: Open has already released what it created.
func (d *Deps) setupCache(cfg config.Config) error {
	if !cfg.Cache.Enabled {
		return nil
	}
	ctx, cancel := context.WithTimeout(d.Context, cacheStartupTimeout)
	defer cancel()

	opts := []cache.Option{
		cache.WithCodecRegistry(d.Codec),
		cache.WithNamespace(cfg.Env, cfg.App.Name),
		cache.WithLogger(&d.Logger),
	}
	if cfg.Database.Tenancy.Enabled {
		opts = append(opts, cache.WithTenantResolver(tenantFromContext))
	}
	c, err := cache.Open(ctx, cfg.Cache, opts...)
	if err != nil {
		return fmt.Errorf("unable to setup cache: %w", err)
	}
	d.Cache = c
	d.addShutdownFunc(c.Shutdown)
	d.Logger.InfoContext(d.Context, "[CacheModule] Cache setup completed", "cache", cfg.Cache,
		"tenant_isolation", cfg.Database.Tenancy.Enabled)
	return nil
}

// tenantFromContext adapts database.TenantFromContext (the framework's
// tenant context, already validated as a UUID v4) to the cache resolver.
func tenantFromContext(ctx context.Context) (string, error) {
	if id, ok := database.TenantFromContext(ctx); ok {
		return id, nil
	}
	return "", database.ErrNoTenant
}

// logConfigWarnings reports what the configuration loader accepted but the
// operator should know about (an unset ${VAR} that expanded to nothing).
func (d *Deps) logConfigWarnings() {
	for _, w := range d.Config.Warnings {
		d.Logger.WarnContext(d.Context, "[ConfigModule] "+w)
	}
}

// NewDeps builds the dependencies of cfg and exits the process with status 1
// when a required module cannot start (see NewDepsE). The reason is always
// written to stderr, whatever the logging configuration, because with logging
// disabled (the default) the logger discards everything.
func NewDeps(cfg config.Config) Deps {
	d, err := NewDepsE(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[Fatal ERROR] %v\n", err)
		os.Exit(1)
	}
	return d
}

// NewDepsE is NewDeps that returns the startup error instead of exiting, for
// embedding and tests. On failure it logs the error through the logger it
// built, shuts down what was already started (so buffered logs and traces are
// flushed) and returns the error joined with any shutdown error; the returned
// Deps is the zero value.
func NewDepsE(cfg config.Config) (Deps, error) {
	ctx := context.Background()

	d := Deps{
		Config:  cfg,
		Context: ctx,
		Aux: &Aux{
			ShutdownFuncs: []func(context.Context) error{},
		},
	}

	if err := d.setupLogger(ctx, cfg); err != nil {
		return Deps{}, d.abort(err)
	}

	if d.Config.HTTP.Server.Header {
		str := fmt.Sprintf(" ***** AMS SOFT nForge v%s: %s v%s *****", nforge.Version, strings.ToUpper(d.Config.App.Name), d.Config.App.Version)
		d.Logger.Info(str)
		d.Logger.Info(strings.Repeat("=", len(str)+1))
	}
	d.Logger.InfoContext(d.Context, "  [LoggerModule] Logger setup completed")

	d.logConfigWarnings()
	for _, step := range []func(config.Config) error{d.setupCodec, d.setupDatabase, d.setupJOSE, d.setupCache} {
		if err := step(cfg); err != nil {
			return Deps{}, d.abort(err)
		}
	}
	return d, nil
}

// abort reports a startup failure through the logger and releases whatever
// was started before it, in reverse order.
func (d *Deps) abort(cause error) error {
	d.Logger.ErrorContext(d.Context, "[Fatal ERROR] "+cause.Error())
	ctx, cancel := context.WithTimeout(context.Background(), abortShutdownTimeout)
	defer cancel()
	return errors.Join(cause, d.Shutdown(ctx))
}
