package scheduler

import (
	"context"
	"log/slog"

	"github.com/ams-soft/nforge/metrics"
	"github.com/ams-soft/nforge/module"
)

// Capability is the application's scheduler, published by Module. A disabled
// scheduler publishes nothing, so a module that requires it fails app.New.
var Capability = module.NewCapability[*Scheduler]("scheduler")

// moduleClock builds the clock of a module instance; tests replace it.
var moduleClock = func() clock { return realClock{} }

// Module is the scheduler module (section scheduler). Registering it is the
// opt-in, and it depends on nothing: no HTTP server, listener or broker. Its
// configuration is validated before any I/O (an invalid time zone, a negative
// cap or the unsupported distributed mode fail app.New).
//
// It is an ingress module: it starts after every module initialized, so no job
// fires before the handlers' dependencies exist, and it stops first, canceling
// the running executions and waiting for them within the shutdown deadline,
// before the database, cache or logger that handlers use are closed. A
// scheduler-only application stays alive in app.Run like any other.
func Module() module.Descriptor {
	return module.Descriptor{ID: "scheduler", Section: "scheduler", Configure: func(src module.Source) (module.Definition, error) {
		var cfg Config
		if err := src.Decode(&cfg); err != nil {
			return module.Definition{}, err
		}
		cfg.ApplyDefaults()
		if !cfg.IsEnabled() {
			return module.Definition{Disabled: true, Provides: []module.Handle{Capability}, Config: cfg}, nil
		}
		loc, err := cfg.Validate()
		if err != nil {
			return module.Definition{}, err
		}
		return module.Definition{
			Provides: []module.Handle{Capability},
			Optional: []module.Handle{module.Logger, metrics.Capability},
			Ingress:  true,
			Config:   cfg,
			Init: func(_ context.Context, sc *module.Scope) (module.Hooks, error) {
				var logger *slog.Logger
				logger, _ = module.Lookup(sc, module.Logger)
				s := newScheduler(cfg, loc, logger, moduleClock())
				if reg, ok := module.Lookup(sc, metrics.Capability); ok {
					s.registerMetrics(reg)
				}
				module.Provide(sc, Capability, s)
				return module.Hooks{
					Start: func(context.Context) error { return s.start(sc.Context()) },
					Ready: s.ready,
					Stop:  s.stop,
				}, nil
			},
		}, nil
	}}
}
