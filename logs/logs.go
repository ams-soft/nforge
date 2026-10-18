package logs

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"time"

	config "github.com/ams-soft/nforge/config"
	"github.com/ams-soft/nforge/telemetry"
	"github.com/lmittmann/tint"
	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	sdklog "go.opentelemetry.io/otel/sdk/log"
)

func Setup(ctx context.Context, config config.Config) (*slog.Logger, func(context.Context) error, error) {
	loglevel := SetLogLevelByEnv(config.Env)

	prettyHandler := tint.NewTextHandler(os.Stdout, &tint.Options{
		Level:      loglevel,
		TimeFormat: time.RFC3339,
		AddSource:  true,
	})

	var handlers []slog.Handler
	if config.Logging.Output.Terminal.Enabled {
		handlers = append(handlers, &leveledHandler{Handler: prettyHandler, level: loglevel})
	}

	shutdown := func(context.Context) error { return nil }

	// Nothing OTel-related (exporter, error handler) is started unless
	// logging.output.otel.enabled is true.
	if config.Logging.Output.Otel.Enabled {
		provider, err := newOtelProvider(ctx, config)
		if err != nil {
			return nil, nil, err
		}
		shutdown = provider.Shutdown

		otelHandler := otelslog.NewHandler(config.App.Name,
			otelslog.WithLoggerProvider(provider),
			otelslog.WithSource(true),
		)
		handlers = append(handlers, &leveledHandler{Handler: otelHandler, level: min(loglevel, slog.LevelInfo)})

		// OTel reports its own failures (e.g. exporter timeouts) through the stdlib
		// log, which slog.SetDefault turns into INFO records that also reach the
		// otel handler. Send them to the terminal only, as warnings: queueing them on
		// the exporter that is failing would just feed it more records.
		terminal := slog.New(&leveledHandler{Handler: prettyHandler, level: loglevel})
		otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
			terminal.Warn(" [LoggerModule] Telemetry export failed", "err", err) //nolint:sloglint // runs later, outside the setup ctx
		}))
	}

	logger := slog.New(&contextHandler{Handler: NewMultiHandler(handlers...)})
	slog.SetDefault(logger)

	return logger, shutdown, nil
}

func newOtelProvider(ctx context.Context, config config.Config) (*sdklog.LoggerProvider, error) {
	var opts []otlploggrpc.Option

	if endpoint := config.Logging.Output.Otel.Endpoint; strings.Contains(endpoint, "://") {
		opts = append(opts, otlploggrpc.WithEndpointURL(endpoint))
	} else if endpoint != "" {
		opts = append(opts, otlploggrpc.WithEndpoint(endpoint))
	}
	if insecure := config.Logging.Output.Otel.Insecure; insecure {
		opts = append(opts, otlploggrpc.WithInsecure())
	}

	exporter, err := otlploggrpc.New(ctx, opts...)
	if err != nil {
		return nil, err
	}

	res, err := telemetry.NewResource(ctx, config)
	if err != nil {
		return nil, err
	}

	return sdklog.NewLoggerProvider(
		sdklog.WithResource(res),
		sdklog.WithProcessor(sdklog.NewBatchProcessor(exporter)),
	), nil
}
