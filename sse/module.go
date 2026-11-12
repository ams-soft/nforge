package sse

import (
	"context"
	"fmt"
	"log/slog"
	"slices"

	"github.com/ams-soft/nforge/codec"
	nhttp "github.com/ams-soft/nforge/http"
	"github.com/ams-soft/nforge/message"
	"github.com/ams-soft/nforge/module"
)

// Capability is the application's SSE manager, published by Module.
var Capability = module.NewCapability[*Manager]("sse")

// Module is the Server-Sent Events module (section sse). Registering it is the
// opt-in: nothing is enabled just because HTTP is. It requires the HTTP and
// codec modules, and the Messaging module when sse.driver is rabbitmq; a
// dependency that is disabled fails app.New, never enabled for you.
//
// It is an ingress module: it accepts streams after the distribution driver is
// active (before that, a stream gets 503 and Retry-After), and it stops before
// the HTTP server drains, closing every open stream first, so a graceful
// shutdown never waits for streams that have no end. It stops before Messaging
// closes the broker connection.
//
// Readiness is the driver's. With rabbitmq a broker outage keeps the open
// streams and their heartbeats alive, publications return
// ErrTransportUnavailable and the module reports not ready (so /ready answers
// 503) until the subscription is restored; mark the module non-critical in
// your probes if a broker outage must not remove the instance from rotation.
func Module() module.Descriptor {
	return module.Descriptor{ID: "sse", Section: "sse", Configure: func(src module.Source) (module.Definition, error) {
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
		app := src.App()
		requires := []module.Handle{nhttp.Capability, codec.Capability}
		if cfg.Driver == DriverRabbitMQ {
			requires = append(requires, message.Capability)
		}
		return module.Definition{
			Provides: []module.Handle{Capability},
			Requires: requires,
			Optional: []module.Handle{module.Logger},
			Ingress:  true,
			Config:   cfg,
			Check: func(v *module.View) error {
				cs, _, _, err := module.ConfigOf[codec.Settings](v, codec.Capability)
				if err != nil {
					return err
				}
				if !slices.Contains(cs.Formats.Enabled, "json") {
					return fmt.Errorf("sse requires \"json\" in codec.formats.enabled")
				}
				if cfg.Driver == DriverRabbitMQ && cfg.RabbitMQ.Exchange == "" {
					mc, _, _, err := module.ConfigOf[message.Config](v, message.Capability)
					if err != nil {
						return err
					}
					cfg.RabbitMQ.Exchange = exchangeName(mc.Namespace, app.Env, app.Name)
				}
				return nil
			},
			Init: func(ctx context.Context, sc *module.Scope) (module.Hooks, error) {
				var logger *slog.Logger
				logger, _ = module.Lookup(sc, module.Logger)
				_ = module.Require(sc, nhttp.Capability) // routes are the application's, registered in its own Init
				var drv driver = &memoryDriver{}
				if cfg.Driver == DriverRabbitMQ {
					drv = &rabbitDriver{client: module.Require(sc, message.Capability), exchange: cfg.RabbitMQ.Exchange, prefetch: cfg.RabbitMQ.Prefetch}
				}
				m := newManager(cfg, module.Require(sc, codec.Capability), logger, drv)
				module.Provide(sc, Capability, m)
				if logger != nil {
					logger.InfoContext(ctx, "[SSEModule] Server-Sent Events ready", "driver", cfg.Driver)
				}
				return module.Hooks{Start: m.start, Ready: m.ready, Stop: m.close}, nil
			},
		}, nil
	}}
}
