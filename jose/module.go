package jose

import (
	"context"
	"fmt"
	"time"

	"github.com/ams-soft/nforge/module"
)

// Capability is the application's JOSE service, published by Module.
var Capability = module.NewCapability[*Service]("jose")

const startupTimeout = 15 * time.Second

// Module is the JOSE module: it owns the jose section and, when jose.enabled
// is true, validates it at configuration time, reads and validates every key
// source at Init (a failure is fatal; the error carries no key path or
// material) and publishes the Service under Capability. Readiness is
// Service.Check (no I/O), Reload re-reads the key files (SIGHUP through
// app.Run) and Stop drains the admitted operations and wipes the secrets.
func Module() module.Descriptor {
	return module.Descriptor{ID: "jose", Section: "jose", Configure: func(src module.Source) (module.Definition, error) {
		var cfg Config
		if err := src.Decode(&cfg); err != nil {
			return module.Definition{}, err
		}
		ApplyDefaults(&cfg)
		if cfg.Enabled {
			if err := ValidateConfig(cfg); err != nil {
				return module.Definition{}, err
			}
		}
		return module.Definition{
			Disabled: !cfg.Enabled,
			Provides: []module.Handle{Capability},
			Optional: []module.Handle{module.Logger},
			Config:   cfg,
			Init: func(ctx context.Context, sc *module.Scope) (module.Hooks, error) {
				openCtx, cancel := context.WithTimeout(ctx, startupTimeout)
				defer cancel()
				service, err := New(openCtx, cfg)
				if err != nil {
					return module.Hooks{}, fmt.Errorf("unable to setup jose: %w", err)
				}
				module.Provide(sc, Capability, service)
				if logger, ok := module.Lookup(sc, module.Logger); ok {
					logger.InfoContext(ctx, "[JOSEModule] JOSE setup completed", "jose", cfg)
				}
				return module.Hooks{Ready: service.Check, Stop: service.Shutdown, Reload: service.Reload}, nil
			},
		}, nil
	}}
}
