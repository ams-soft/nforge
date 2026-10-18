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
func (d *Deps) setupLogger(ctx context.Context, config config.Config) bool {
	if config.IsLoggingEnabled() {
		logger, shutdown, err := logs.Setup(ctx, config)
		if err != nil {
			slog.Error("[Fatal ERROR] unable to setup logger", "error", err)
			os.Exit(1)
		}
		d.addShutdownFunc(shutdown)

		if config.IsTracingEnabled() {
			shutdownTracing, err := telemetry.SetupTracing(ctx, config)
			if err != nil {
				slog.Error("[Fatal ERROR] unable to setup tracing", "error", err)
				panic(err)
			}
			d.addShutdownFunc(shutdownTracing)
		}

		d.Logger = *logger
	} else {
		d.Logger = *logs.NewNoop()
	}
	return true
}

// setupCodec builds the Registry from the config and installs it as the
// process default, which is the one used by the http helpers (SendJSON, ParseXML...).
func (d *Deps) setupCodec(cfg config.Config) {
	registry, err := codec.New(codec.Config{
		Formats:             cfg.Codec.Formats.Enabled,
		Default:             cfg.Codec.Default,
		StrictUnknownFields: cfg.Codec.StrictUnknownFields,
		MaxPayloadBytes:     int64(cfg.Codec.MaxPayloadBytes),
		AllowDTD:            cfg.Codec.XML.DisallowDTD != nil && !*cfg.Codec.XML.DisallowDTD,
	})
	if err != nil {
		d.Logger.ErrorContext(d.Context, "  [Fatal ERROR] unable to setup codec", "error", err)
		os.Exit(1)
	}
	codec.SetDefault(registry)
	d.Codec = registry
	d.Logger.InfoContext(d.Context, "  [CodecModule] Codec setup completed")
}

// setupDatabase opens the pool (with a startup ping) when database.enabled is
// true. A connection failure is fatal: the application does not start without the database.
func (d *Deps) setupDatabase(cfg config.Config) {
	if !cfg.Database.Enabled {
		return
	}
	ctx, cancel := context.WithTimeout(d.Context, databaseStartupTimeout)
	defer cancel()

	db, err := database.Open(ctx, cfg.Database)
	if err != nil {
		d.Logger.ErrorContext(d.Context, "[Fatal ERROR] unable to setup database", "error", err)
		os.Exit(1)
	}
	d.DB = db
	d.addShutdownFunc(db.Shutdown)
	d.Logger.InfoContext(d.Context, "[DatabaseModule] Database setup completed")
}

// setupJOSE loads the JOSE keys when jose.enabled is true. A failure is
// fatal: the application does not start with broken key material. The error
// is already sanitized by the jose package (no paths or key bytes).
func (d *Deps) setupJOSE(cfg config.Config) {
	if !cfg.JOSE.Enabled {
		return
	}
	ctx, cancel := context.WithTimeout(d.Context, joseStartupTimeout)
	defer cancel()

	service, err := jose.New(ctx, cfg.JOSE)
	if err != nil {
		d.Logger.ErrorContext(d.Context, "[Fatal ERROR] unable to setup jose", "error", err)
		os.Exit(1)
	}
	d.JOSE = service
	d.addShutdownFunc(service.Shutdown)
	d.Logger.InfoContext(d.Context, "[JOSEModule] JOSE setup completed", "jose", cfg.JOSE)
}

// setupCache opens the cache when cache.enabled is true, after the codec
// (objects use d.Codec) and the database (so LIFO closes the cache first).
// With database.tenancy.enabled the keys are isolated per tenant, read from
// the same context value as the database (database.TenantFromContext, set
// by the nhttp tenant middlewares). A startup failure (e.g. Redis down) is
// fatal, like the database: Open has already released what it created.
func (d *Deps) setupCache(cfg config.Config) {
	if !cfg.Cache.Enabled {
		return
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
		d.Logger.ErrorContext(d.Context, "[Fatal ERROR] unable to setup cache", "error", err)
		os.Exit(1)
	}
	d.Cache = c
	d.addShutdownFunc(c.Shutdown)
	d.Logger.InfoContext(d.Context, "[CacheModule] Cache setup completed", "cache", cfg.Cache,
		"tenant_isolation", cfg.Database.Tenancy.Enabled)
}

// tenantFromContext adapts database.TenantFromContext (the framework's
// tenant context, already validated as a UUID v4) to the cache resolver.
func tenantFromContext(ctx context.Context) (string, error) {
	if id, ok := database.TenantFromContext(ctx); ok {
		return id, nil
	}
	return "", database.ErrNoTenant
}

func NewDeps(config config.Config) Deps {
	ctx := context.Background()

	deps := Deps{
		Config:  config,
		Context: ctx,
		Aux: &Aux{
			ShutdownFuncs: []func(context.Context) error{},
		},
	}

	ok := deps.setupLogger(ctx, config)

	nforgeVersion := nforge.Version

	if deps.Config.HTTP.Server.Header {
		str := fmt.Sprintf(" ***** AMS SOFT nForge v%s: %s v%s *****", nforgeVersion, strings.ToUpper(deps.Config.App.Name), deps.Config.App.Version)
		deps.Logger.Info(str)
		deps.Logger.Info(strings.Repeat("=", len(str)+1))
	}
	if ok {
		deps.Logger.InfoContext(deps.Context, "  [LoggerModule] Logger setup completed")
	}

	deps.setupCodec(config)
	deps.setupDatabase(config)
	deps.setupJOSE(config)
	deps.setupCache(config)

	return deps
}
