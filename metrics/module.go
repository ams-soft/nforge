package metrics

import (
	"context"

	"github.com/ams-soft/nforge/module"
)

// Capability is the registry published by Module.
var Capability = module.NewCapability[*Registry]("metrics")

// Config is the metrics section.
type Config struct {
	// Enabled is true when omitted; false keeps a registered module out of the run.
	Enabled *bool `yaml:"enabled"`
}

// Module owns the metrics section and publishes the Registry. Registering it is
// the opt-in; the framework's modules that have instruments to offer declare
// the capability as optional and register them when it is present. To expose
// the registry over HTTP enable http.routes.metrics.
func Module() module.Descriptor {
	return module.Descriptor{ID: "metrics", Section: "metrics", Configure: func(src module.Source) (module.Definition, error) {
		var cfg Config
		if err := src.Decode(&cfg); err != nil {
			return module.Definition{}, err
		}
		if cfg.Enabled != nil && !*cfg.Enabled {
			return module.Definition{Disabled: true, Provides: []module.Handle{Capability}, Config: cfg}, nil
		}
		return module.Definition{
			Provides: []module.Handle{Capability},
			Config:   cfg,
			Init: func(_ context.Context, sc *module.Scope) (module.Hooks, error) {
				module.Provide(sc, Capability, New())
				return module.Hooks{}, nil
			},
		}, nil
	}}
}
