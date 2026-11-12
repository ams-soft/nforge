package nhttp

import (
	"log/slog"
	"time"

	"github.com/ams-soft/nforge/codec"
	"github.com/ams-soft/nforge/jose"
	"github.com/ams-soft/nforge/logs"
	"github.com/ams-soft/nforge/metrics"
	"github.com/ams-soft/nforge/module"
	"github.com/ams-soft/nforge/telemetry"
	"go.opentelemetry.io/otel"
)

// testConfig is the aggregate configuration the tests of this package build by
// hand, one section per module.
type testConfig struct {
	Env string
	App struct {
		Name, Version string
		InternalUse   bool
	}
	HTTP    Config
	JOSE    jose.Config
	Codec   codec.Settings
	Tracing telemetry.Settings
	Logging logs.Settings
}

// testDeps are the collaborators of a server built without an application.
type testDeps struct {
	Config  testConfig
	Logger  *slog.Logger
	JOSE    *jose.Service
	Codec   *codec.Registry
	Status  module.Introspection
	Metrics *metrics.Registry
}

// buildServer builds a server from a hand-made configuration, as the module
// would from its sections: request logging only with a logging output, tracing
// only with OTel on, through the global provider.
func buildServer(d *testDeps) *Server {
	app := module.App{Name: d.Config.App.Name, Version: d.Config.App.Version, Env: d.Config.Env,
		InternalUse: d.Config.App.InternalUse, OnlineSince: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	deps := Dependencies{App: app, Logger: d.Logger, AccessLog: d.Config.Logging.Enabled(), Codec: d.Codec, JOSE: d.JOSE, Status: d.Status, Metrics: d.Metrics}
	if d.Config.Logging.Output.Otel.Enabled && d.Config.Tracing.Otel.IsEnabled() {
		deps.Tracing = &telemetry.Tracing{Provider: otel.GetTracerProvider(), TrustIncoming: d.Config.Tracing.Otel.TrustsIncoming()}
	}
	if c := d.Config.Codec; deps.Codec == nil && (len(c.Formats.Enabled) > 0 || c.MaxPayloadBytes > 0 || c.StrictUnknownFields) {
		c.ApplyDefaults()
		r, err := codec.New(c.Config())
		if err != nil {
			panic(err)
		}
		deps.Codec = r
	}
	return New(d.Config.HTTP, deps)
}
