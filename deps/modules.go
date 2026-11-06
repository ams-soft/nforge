package deps

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/ams-soft/nforge"
	"github.com/ams-soft/nforge/cache"
	"github.com/ams-soft/nforge/codec"
	"github.com/ams-soft/nforge/config"
	"github.com/ams-soft/nforge/database"
	"github.com/ams-soft/nforge/jose"
	"github.com/ams-soft/nforge/logs"
	"github.com/ams-soft/nforge/module"
	"github.com/ams-soft/nforge/telemetry"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
)

// Module is a module of an nForge application; see Bootstrap.
type Module = module.Module[config.Config]

// The capabilities of the built-in modules. A module passed to Bootstrap names
// them in Requires or Optional and reads them with module.Require or
// module.Lookup, so it shares the resource instead of opening a second one.
var (
	CapLogger   = module.NewCapability[*slog.Logger]("logger")
	CapTracing  = module.NewCapability[trace.TracerProvider]("tracing")
	CapCodec    = module.NewCapability[*codec.Registry]("codec")
	CapDatabase = module.NewCapability[*database.DB]("database")
	CapJOSE     = module.NewCapability[*jose.Service]("jose")
	CapCache    = module.NewCapability[*cache.Cache]("cache")
)

const (
	databaseStartupTimeout = 15 * time.Second
	joseStartupTimeout     = 15 * time.Second
	cacheStartupTimeout    = 15 * time.Second
)

// caps is shorthand for the Requires and Optional functions of a module whose
// list does not depend on the configuration.
func caps(ids ...module.CapabilityID) func(config.Config) []module.CapabilityID {
	return func(config.Config) []module.CapabilityID { return ids }
}

// builtinModules lists what the framework manages. The order is the tie-break of
// the start order, so it is what keeps the sequence of v0.2.0: logger, tracing,
// the startup report, codec, database, jose, cache. Shutdown runs it backwards.
func builtinModules(isolated bool) []Module {
	b := builtins{isolated: isolated}
	logger, tracing, codecCap := CapLogger.ID(), CapTracing.ID(), CapCodec.ID()
	return []Module{
		{ID: "logger", Provides: []module.CapabilityID{logger}, Init: b.initLogger},
		{
			ID:       "tracing",
			Provides: []module.CapabilityID{tracing},
			Enabled:  func(cfg config.Config) bool { return cfg.IsTracingEnabled() },
			Requires: caps(logger),
			Init:     b.initTracing,
		},
		{
			// It logs, so it follows the logger, and after tracing because a tracing
			// failure must not print the banner of an application that never ran.
			ID:       "startup-report",
			Requires: caps(logger),
			Optional: caps(tracing),
			Init:     initStartupReport,
		},
		{ID: "codec", Provides: []module.CapabilityID{codecCap}, Requires: caps(logger), Init: b.initCodec},
		{
			ID:       "database",
			Provides: []module.CapabilityID{CapDatabase.ID()},
			Enabled:  func(cfg config.Config) bool { return cfg.Database.Enabled },
			Requires: caps(logger),
			Init:     initDatabase,
		},
		{
			ID:       "jose",
			Provides: []module.CapabilityID{CapJOSE.ID()},
			Enabled:  func(cfg config.Config) bool { return cfg.JOSE.Enabled },
			Requires: caps(logger),
			Init:     initJOSE,
		},
		{
			// The database is optional and only orders the two, so the cache closes
			// first: tenant isolation reads the context, not the database module,
			// and works with database.enabled: false.
			ID:       "cache",
			Provides: []module.CapabilityID{CapCache.ID()},
			Enabled:  func(cfg config.Config) bool { return cfg.Cache.Enabled },
			Requires: caps(logger, codecCap),
			Optional: caps(CapDatabase.ID()),
			Init:     initCache,
		},
		{
			// Passive: nhttp.ServerHttp owns the listeners and calls Deps.Shutdown
			// once they are released, so a second lifecycle here would race it. The
			// node records what the server consumes, and fails the bootstrap when
			// http.auth.default_profile names a profile no enabled jose can serve.
			ID: "http",
			Requires: func(cfg config.Config) []module.CapabilityID {
				required := []module.CapabilityID{logger, codecCap}
				if cfg.HTTP.Auth.DefaultProfile != "" {
					required = append(required, CapJOSE.ID())
				}
				return required
			},
			Optional: caps(tracing, CapDatabase.ID(), CapJOSE.ID(), CapCache.ID()),
		},
	}
}

// builtins are the Init functions that differ when the process globals are off.
type builtins struct{ isolated bool }

func (b builtins) initLogger(ctx context.Context, cfg config.Config, s *module.Scope) (module.Hooks, error) {
	if !cfg.IsLoggingEnabled() {
		module.Provide(s, CapLogger, logs.NewNoop())
		return module.Hooks{}, nil
	}
	var opts []logs.Option
	if b.isolated {
		opts = append(opts, logs.WithoutGlobals())
	}
	logger, shutdown, err := logs.Setup(ctx, cfg, opts...)
	if err != nil {
		return module.Hooks{}, fmt.Errorf("unable to setup logger: %w", err)
	}
	module.Provide(s, CapLogger, logger)
	return module.Hooks{Shutdown: shutdown}, nil
}

func (b builtins) initTracing(ctx context.Context, cfg config.Config, s *module.Scope) (module.Hooks, error) {
	provider, err := telemetry.NewTracerProvider(ctx, cfg)
	if err != nil {
		return module.Hooks{}, fmt.Errorf("unable to setup tracing: %w", err)
	}
	if !b.isolated {
		otel.SetTextMapPropagator(telemetry.Propagator())
		otel.SetTracerProvider(provider)
	}
	module.Provide(s, CapTracing, trace.TracerProvider(provider))
	return module.Hooks{Shutdown: provider.Shutdown}, nil
}

func initStartupReport(ctx context.Context, cfg config.Config, s *module.Scope) (module.Hooks, error) {
	logger := module.Require(s, CapLogger)
	if cfg.HTTP.Server.Header {
		str := fmt.Sprintf(" ***** AMS SOFT nForge v%s: %s v%s *****", nforge.Version, strings.ToUpper(cfg.App.Name), cfg.App.Version)
		logger.InfoContext(ctx, str)
		logger.InfoContext(ctx, strings.Repeat("=", len(str)+1))
	}
	logger.InfoContext(ctx, "  [LoggerModule] Logger setup completed")
	logConfigWarnings(ctx, logger, cfg)
	return module.Hooks{}, nil
}

// logConfigWarnings reports what the configuration loader accepted but the
// operator should know about (an unset ${VAR} that expanded to nothing).
func logConfigWarnings(ctx context.Context, logger *slog.Logger, cfg config.Config) {
	// env prd filters the log below ERROR, so a WARN would never reach the
	// terminal; the operator must see these at least once.
	level := slog.LevelWarn
	if cfg.Env == "prd" {
		level = slog.LevelError
	}
	for _, w := range cfg.Warnings {
		logger.Log(ctx, level, "[ConfigModule] "+w)
	}
}

// initCodec builds the Registry from the config and, unless isolated, installs it
// as the process default, which the http helpers (SendJSON, ParseXML...) use.
func (b builtins) initCodec(ctx context.Context, cfg config.Config, s *module.Scope) (module.Hooks, error) {
	registry, err := codec.New(codec.Config{
		Formats:             cfg.Codec.Formats.Enabled,
		Default:             cfg.Codec.Default,
		StrictUnknownFields: cfg.Codec.StrictUnknownFields,
		MaxPayloadBytes:     int64(cfg.Codec.MaxPayloadBytes),
		AllowDTD:            cfg.Codec.XML.DisallowDTD != nil && !*cfg.Codec.XML.DisallowDTD,
	})
	if err != nil {
		return module.Hooks{}, fmt.Errorf("unable to setup codec: %w", err)
	}
	if !b.isolated {
		codec.SetDefault(registry)
	}
	module.Provide(s, CapCodec, registry)
	module.Require(s, CapLogger).InfoContext(ctx, "  [CodecModule] Codec setup completed")
	return module.Hooks{}, nil
}

// initDatabase opens the pool, with a startup ping. A connection failure is
// fatal: the application does not start without the database.
func initDatabase(ctx context.Context, cfg config.Config, s *module.Scope) (module.Hooks, error) {
	openCtx, cancel := context.WithTimeout(ctx, databaseStartupTimeout)
	defer cancel()

	db, err := database.Open(openCtx, cfg.Database)
	if err != nil {
		return module.Hooks{}, fmt.Errorf("unable to setup database: %w", err)
	}
	module.Provide(s, CapDatabase, db)
	module.Require(s, CapLogger).InfoContext(ctx, "[DatabaseModule] Database setup completed")
	return module.Hooks{Ready: db.Ping, Shutdown: db.Shutdown}, nil
}

// initJOSE loads the keys. A failure is fatal: the application does not start
// with broken key material. The error is already sanitized by the jose package
// (no paths or key bytes).
func initJOSE(ctx context.Context, cfg config.Config, s *module.Scope) (module.Hooks, error) {
	openCtx, cancel := context.WithTimeout(ctx, joseStartupTimeout)
	defer cancel()

	service, err := jose.New(openCtx, cfg.JOSE)
	if err != nil {
		return module.Hooks{}, fmt.Errorf("unable to setup jose: %w", err)
	}
	module.Provide(s, CapJOSE, service)
	module.Require(s, CapLogger).InfoContext(ctx, "[JOSEModule] JOSE setup completed", "jose", cfg.JOSE)
	return module.Hooks{Ready: service.Check, Shutdown: service.Shutdown}, nil
}

// initCache opens the cache on the shared codec registry. With
// database.tenancy.enabled the keys are isolated per tenant, read from the same
// context value as the database (database.TenantFromContext, set by the nhttp
// tenant middlewares). A startup failure (e.g. Redis down) is fatal, like the
// database: Open has already released what it created.
func initCache(ctx context.Context, cfg config.Config, s *module.Scope) (module.Hooks, error) {
	openCtx, cancel := context.WithTimeout(ctx, cacheStartupTimeout)
	defer cancel()

	logger := module.Require(s, CapLogger)
	opts := []cache.Option{
		cache.WithCodecRegistry(module.Require(s, CapCodec)),
		cache.WithNamespace(cfg.Env, cfg.App.Name),
		cache.WithLogger(logger),
	}
	if cfg.Database.Tenancy.Enabled {
		opts = append(opts, cache.WithTenantResolver(tenantFromContext))
	}
	c, err := cache.Open(openCtx, cfg.Cache, opts...)
	if err != nil {
		return module.Hooks{}, fmt.Errorf("unable to setup cache: %w", err)
	}
	module.Provide(s, CapCache, c)
	logger.InfoContext(ctx, "[CacheModule] Cache setup completed", "cache", cfg.Cache,
		"tenant_isolation", cfg.Database.Tenancy.Enabled)
	return module.Hooks{Ready: c.Ping, Shutdown: c.Shutdown}, nil
}

// tenantFromContext adapts database.TenantFromContext (the framework's tenant
// context, already validated as a UUID v4) to the cache resolver.
func tenantFromContext(ctx context.Context) (string, error) {
	if id, ok := database.TenantFromContext(ctx); ok {
		return id, nil
	}
	return "", database.ErrNoTenant
}
