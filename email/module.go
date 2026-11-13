package email

import (
	"context"
	"log/slog"

	"github.com/ams-soft/nforge/metrics"
	"github.com/ams-soft/nforge/module"
	"github.com/ams-soft/nforge/telemetry"
)

// Capability is the application's email client, published by Module. A
// disabled module publishes nothing, so a module that requires it fails app.New.
var Capability = module.NewCapability[*Client]("email")

// newDriverFor builds the driver of a module instance; tests replace it.
var newDriverFor = newDriver

// Module is the email module (section email). It depends on nothing: no HTTP,
// database or broker; logger, metrics and tracing are optional. It is not
// ingress. Its configuration is validated before any I/O and the SMTP server
// is first contacted by Send: starting the application never dials it, and
// readiness reflects the local client only (it never sends a test message).
//
// Stop refuses new sends and waits for the sends in flight until the shutdown
// deadline, then cancels them.
func Module() module.Descriptor {
	return module.Descriptor{ID: "email", Section: "email", Configure: func(src module.Source) (module.Definition, error) {
		var cfg Config
		if err := src.Decode(&cfg); err != nil {
			return module.Definition{}, err
		}
		cfg.ApplyDefaults()
		if !cfg.IsEnabled() {
			return module.Definition{Disabled: true, Provides: []module.Handle{Capability}, Config: cfg}, nil
		}
		if err := cfg.Validate(); err != nil {
			return module.Definition{}, err
		}
		return module.Definition{
			Provides: []module.Handle{Capability},
			Optional: []module.Handle{module.Logger, metrics.Capability, telemetry.Capability},
			Config:   cfg,
			Init: func(ctx context.Context, sc *module.Scope) (module.Hooks, error) {
				var logger *slog.Logger
				logger, _ = module.Lookup(sc, module.Logger)
				drv, err := newDriverFor(cfg)
				if err != nil {
					return module.Hooks{}, err
				}
				c := newClient(cfg, drv, logger)
				if reg, ok := module.Lookup(sc, metrics.Capability); ok {
					c.registerMetrics(reg)
				}
				if tr, ok := module.Lookup(sc, telemetry.Capability); ok {
					c.tracer = tr.Provider.Tracer("github.com/ams-soft/nforge/email")
				}
				if cfg.Driver == DriverNoop {
					c.logger.WarnContext(ctx, "[EmailModule] Driver noop: messages are validated and discarded, nothing is sent")
				} else if cfg.SMTP.Username == "" {
					c.logger.InfoContext(ctx, "[EmailModule] No SMTP credentials configured: messages are sent without AUTH (relay trusted by network)")
				}
				module.Provide(sc, Capability, c)
				return module.Hooks{
					Start: func(context.Context) error { c.start(); return nil },
					Ready: c.ready,
					Stop:  c.stop,
				}, nil
			},
		}, nil
	}}
}
