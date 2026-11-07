package logs

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/ams-soft/nforge/module"
	"github.com/ams-soft/nforge/telemetry"
	"github.com/lmittmann/tint"
	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	sdklog "go.opentelemetry.io/otel/sdk/log"
)

// Option configures Setup.
type Option func(*options)

type options struct{ globals bool }

// WithProcessGlobals also installs the logger as slog.Default and routes the
// OTel error handler to the terminal. Without it the process-wide state is left
// alone, so several loggers can coexist in one process; OTel failures then go
// to OTel's default handler.
func WithProcessGlobals() Option { return func(o *options) { o.globals = true } }

// Setup builds the logger of the logging settings: the terminal output (tint)
// and the OTel log export, each optional, under a handler that adds the
// request_id of the context. The level follows app.Env. It returns the flush
// of the OTel exporter.
func Setup(ctx context.Context, s Settings, app module.App, opts ...Option) (*slog.Logger, func(context.Context) error, error) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	loglevel := SetLogLevelByEnv(app.Env)

	prettyHandler := tint.NewTextHandler(os.Stdout, &tint.Options{
		Level:      loglevel,
		TimeFormat: time.RFC3339,
		AddSource:  loglevel == slog.LevelDebug, // file:line in every record only while developing
	})

	var handlers []slog.Handler
	if s.Output.Terminal.Enabled {
		handlers = append(handlers, &leveledHandler{Handler: prettyHandler, level: loglevel})
	}

	shutdown := func(context.Context) error { return nil }

	// Nothing OTel-related (exporter, error handler) is started unless
	// logging.output.otel.enabled is true.
	if s.Output.Otel.Enabled {
		provider, err := newOtelProvider(ctx, s, app)
		if err != nil {
			return nil, nil, err
		}
		shutdown = provider.Shutdown

		otelHandler := otelslog.NewHandler(app.Name,
			otelslog.WithLoggerProvider(provider),
			otelslog.WithSource(loglevel == slog.LevelDebug),
		)
		handlers = append(handlers, &leveledHandler{Handler: otelHandler, level: min(loglevel, slog.LevelInfo)})

		// OTel reports its own failures (e.g. exporter timeouts) through the stdlib
		// log, which slog.SetDefault turns into INFO records that also reach the
		// otel handler. Send them to the terminal only, as warnings: queueing them on
		// the exporter that is failing would just feed it more records.
		if o.globals {
			terminal := slog.New(&leveledHandler{Handler: prettyHandler, level: loglevel})
			otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
				terminal.Warn(" [LoggerModule] Telemetry export failed", "err", err) //nolint:sloglint // runs later, outside the setup ctx
			}))
		}
	}

	logger := slog.New(&contextHandler{Handler: NewMultiHandler(handlers...)})
	if o.globals {
		slog.SetDefault(logger)
	}

	return logger, shutdown, nil
}

func newOtelProvider(ctx context.Context, s Settings, app module.App) (*sdklog.LoggerProvider, error) {
	var opts []otlploggrpc.Option

	if endpoint := s.Output.Otel.Endpoint; strings.Contains(endpoint, "://") {
		opts = append(opts, otlploggrpc.WithEndpointURL(endpoint))
	} else if endpoint != "" {
		opts = append(opts, otlploggrpc.WithEndpoint(endpoint))
	}
	if s.Output.Otel.Insecure {
		opts = append(opts, otlploggrpc.WithInsecure())
	}

	exporter, err := otlploggrpc.New(ctx, opts...)
	if err != nil {
		return nil, err
	}

	res, err := telemetry.NewResource(ctx, app)
	if err != nil {
		return nil, err
	}

	return sdklog.NewLoggerProvider(
		sdklog.WithResource(res),
		sdklog.WithProcessor(sdklog.NewBatchProcessor(exporter)),
	), nil
}
