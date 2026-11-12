package cache

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/ams-soft/nforge/codec"
	"github.com/ams-soft/nforge/database"
	"github.com/ams-soft/nforge/metrics"
	"github.com/ams-soft/nforge/module"
)

// Capability is the application's cache, published by Module.
var Capability = module.NewCapability[*Cache]("cache")

const startupTimeout = 15 * time.Second

// UsesTLS reports whether the configured Redis connection is encrypted.
func (cfg Config) UsesTLS() bool {
	if cfg.Redis.URL != "" {
		u, err := url.Parse(cfg.Redis.URL)
		return err == nil && u.Scheme == "rediss"
	}
	return cfg.Redis.TLS.Enabled
}

// plaintextCredentials reports Redis credentials that would travel in clear
// text to a host that is not this machine.
func (cfg Config) plaintextCredentials() bool {
	if cfg.Driver != DriverRedis {
		return false
	}
	host, hasCreds := cfg.Redis.Host, cfg.Redis.Username != "" || cfg.Redis.Password != ""
	if cfg.Redis.URL != "" {
		u, err := url.Parse(cfg.Redis.URL)
		if err != nil {
			return false // ValidateConfig rejects it
		}
		_, hasPassword := u.User.Password()
		host, hasCreds = u.Hostname(), u.User != nil && (u.User.Username() != "" || hasPassword)
	}
	return hasCreds && !cfg.UsesTLS() && !isLocalHost(host)
}

func isLocalHost(host string) bool {
	if host == "" || strings.HasPrefix(host, "/") || strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// Module is the cache module: it owns the cache section and, when
// cache.enabled is true, opens the cache at Init on the application's codec
// Registry (with a startup ping; Redis down is fatal) and publishes it under
// Capability. Keys are namespaced by env and app.name unless cache.prefix is
// set. When the registered database module has database.tenancy.enabled
// (whether or not the database itself is enabled) the keys are isolated per
// tenant, read from the framework's tenant context (database.TenantFromContext,
// set by the nhttp tenant middlewares), and a call without a tenant fails
// before any I/O.
func Module() module.Descriptor {
	return module.Descriptor{ID: "cache", Section: "cache", Configure: func(src module.Source) (module.Definition, error) {
		var cfg Config
		if err := src.Decode(&cfg); err != nil {
			return module.Definition{}, err
		}
		ApplyDefaults(&cfg)
		if cfg.Enabled {
			if err := ValidateConfig(cfg); err != nil {
				return module.Definition{}, err
			}
			if cfg.plaintextCredentials() {
				src.Warn("redis_plaintext", "cache.redis sends its credentials without TLS to a host that is not local; use rediss:// or cache.redis.tls.enabled")
			}
		}
		app := src.App()
		tenants := false
		return module.Definition{
			Disabled: !cfg.Enabled,
			Provides: []module.Handle{Capability},
			Requires: []module.Handle{codec.Capability},
			// The database is optional: it only orders the two (the cache closes
			// first) and tells whether keys are isolated per tenant.
			Optional: []module.Handle{module.Logger, database.Capability, metrics.Capability},
			Config:   cfg,
			Check: func(v *module.View) error {
				settings, _, _, err := module.ConfigOf[codec.Settings](v, codec.Capability)
				if err != nil {
					return err
				}
				// Cached objects are read back through the registry, which refuses
				// payloads over codec.max_payload_bytes: a larger cached value could
				// be stored but never decoded again.
				if limit := int64(settings.MaxPayloadBytes); int64(cfg.Limits.MaxValueBytes) > limit {
					return fmt.Errorf("%w: cache.limits.max_value_bytes (%d) must not exceed codec.max_payload_bytes (%d), "+
						"or values larger than the codec limit would be stored and never read back",
						ErrInvalidConfig, int64(cfg.Limits.MaxValueBytes), limit)
				}
				if !settings.Enables(cfg.Codec) {
					return fmt.Errorf("%w: cache.codec %q must be listed in codec.formats.enabled", ErrUnsupportedCodec, cfg.Codec)
				}
				db, _, found, err := module.ConfigOf[database.Config](v, database.Capability)
				if err != nil {
					return err
				}
				tenants = found && db.Tenancy.Enabled
				return nil
			},
			Init: func(ctx context.Context, sc *module.Scope) (module.Hooks, error) {
				openCtx, cancel := context.WithTimeout(ctx, startupTimeout)
				defer cancel()
				opts := []Option{
					WithCodecRegistry(module.Require(sc, codec.Capability)),
					WithNamespace(app.Env, app.Name),
				}
				logger, hasLogger := module.Lookup(sc, module.Logger)
				if hasLogger {
					opts = append(opts, WithLogger(logger))
				}
				if tenants {
					opts = append(opts, WithTenantResolver(tenantFromContext))
				}
				c, err := Open(openCtx, cfg, opts...)
				if err != nil {
					return module.Hooks{}, fmt.Errorf("unable to setup cache: %w", err)
				}
				module.Provide(sc, Capability, c)
				if reg, ok := module.Lookup(sc, metrics.Capability); ok {
					c.registerMetrics(reg)
				}
				if hasLogger {
					logger.InfoContext(ctx, "[CacheModule] Cache setup completed", "cache", cfg, "tenant_isolation", tenants)
				}
				return module.Hooks{Ready: c.Ping, Stop: c.Shutdown}, nil
			},
		}, nil
	}}
}

// tenantFromContext adapts database.TenantFromContext (the framework's tenant
// context, already validated as a UUID) to the cache resolver.
func tenantFromContext(ctx context.Context) (string, error) {
	if id, ok := database.TenantFromContext(ctx); ok {
		return id, nil
	}
	return "", database.ErrNoTenant
}
