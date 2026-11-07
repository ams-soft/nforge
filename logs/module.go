package logs

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/ams-soft/nforge/module"
	"github.com/ams-soft/nforge/telemetry"
)

// Settings is the logging configuration section.
type Settings struct {
	Output OutputSettings `yaml:"output"`
}

// OutputSettings selects the outputs; with none the logger discards everything.
type OutputSettings struct {
	Otel     OtelOutput     `yaml:"otel"`
	Terminal TerminalOutput `yaml:"terminal"`
}

// OtelOutput exports the logs over OTLP/gRPC.
type OtelOutput struct {
	Enabled  bool   `yaml:"enabled"`  // default: false
	Endpoint string `yaml:"endpoint"` // "host:port" or URL (http:// implies insecure); default: "localhost:4317"
	Insecure bool   `yaml:"insecure"` // default: false
}

// TerminalOutput writes colored text to stdout.
type TerminalOutput struct {
	Enabled bool `yaml:"enabled"` // default: false
}

// Enabled reports whether any output is on.
func (s Settings) Enabled() bool { return s.Output.Terminal.Enabled || s.Output.Otel.Enabled }

// Redacted returns a copy without the endpoint's credentials.
func (s Settings) Redacted() Settings {
	s.Output.Otel.Endpoint = telemetry.RedactEndpoint(s.Output.Otel.Endpoint)
	return s
}

type plainSettings Settings

// String prints the redacted settings.
func (s Settings) String() string { return fmt.Sprintf("%+v", plainSettings(s.Redacted())) }

// GoString prints the redacted settings.
func (s Settings) GoString() string { return fmt.Sprintf("%#v", plainSettings(s.Redacted())) }

// LogValue logs the redacted settings.
func (s Settings) LogValue() slog.Value { return slog.AnyValue(plainSettings(s.Redacted())) }

// MarshalJSON marshals the redacted settings.
func (s Settings) MarshalJSON() ([]byte, error) { return json.Marshal(plainSettings(s.Redacted())) }

// Module is the logging module: it owns the logging section and publishes the
// application logger under module.Logger, which every other module declares as
// an optional dependency. With no output enabled the logger discards
// everything. With app.WithProcessGlobals it is also slog.Default. It stops
// last, after every module that logs, and flushes the OTel exporter.
func Module() module.Descriptor {
	return module.Descriptor{ID: "logger", Section: "logging", Configure: func(src module.Source) (module.Definition, error) {
		var s Settings
		if err := src.Decode(&s); err != nil {
			return module.Definition{}, err
		}
		app, globals := src.App(), src.ProcessGlobals()
		return module.Definition{
			Provides: []module.Handle{module.Logger},
			Config:   s,
			Init: func(ctx context.Context, sc *module.Scope) (module.Hooks, error) {
				if !s.Enabled() {
					module.Provide(sc, module.Logger, NewNoop())
					return module.Hooks{}, nil
				}
				var opts []Option
				if globals {
					opts = append(opts, WithProcessGlobals())
				}
				logger, flush, err := Setup(ctx, s, app, opts...)
				if err != nil {
					return module.Hooks{}, fmt.Errorf("unable to setup logger: %w", err)
				}
				module.Provide(sc, module.Logger, logger)
				logger.InfoContext(ctx, "[LoggerModule] Logger setup completed")
				return module.Hooks{Stop: flush}, nil
			},
		}, nil
	}}
}
