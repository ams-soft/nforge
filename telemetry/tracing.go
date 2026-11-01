package telemetry

import (
	"context"
	"strings"

	config "github.com/ams-soft/nforge/config"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// SetupTracing registers the global TracerProvider and propagator (W3C
// traceparent + baggage). When tracing is off (see Config.IsTracingEnabled) no
// TracerProvider is registered and otel stays on the no-op provider: spans are
// not exported, but instrumented code keeps working.
func SetupTracing(ctx context.Context, config config.Config) (func(context.Context) error, error) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	if !config.IsTracingEnabled() {
		return func(context.Context) error { return nil }, nil
	}

	cfg := config.Tracing.Otel
	var opts []otlptracegrpc.Option
	// Same rule as the logs exporter: a full URL goes through WithEndpointURL, "host:port" through WithEndpoint.
	if endpoint := cfg.Endpoint; strings.Contains(endpoint, "://") {
		opts = append(opts, otlptracegrpc.WithEndpointURL(endpoint))
	} else if endpoint != "" {
		opts = append(opts, otlptracegrpc.WithEndpoint(endpoint))
	}
	if cfg.Insecure {
		opts = append(opts, otlptracegrpc.WithInsecure())
	}

	exporter, err := otlptracegrpc.New(ctx, opts...)
	if err != nil {
		return nil, err
	}

	res, err := NewResource(ctx, config)
	if err != nil {
		return nil, err
	}

	provider := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithBatcher(exporter),
		sdktrace.WithIDGenerator(IDGenerator()), // trace_id taken from X-Request-Id
		sdktrace.WithSampler(newSampler(cfg.Ratio())),
	)
	otel.SetTracerProvider(provider)

	return provider.Shutdown, nil
}

// newSampler records the given fraction of traces, chosen by trace id, and
// follows the sampling decision of a parent when there is one.
func newSampler(ratio float64) sdktrace.Sampler {
	return sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio))
}

// IDGenerator returns the generator that takes the trace id of a root span
// from the context (ContextWithTraceID), used by the tracing provider and by
// tests of code that depends on request_id = trace_id.
func IDGenerator() sdktrace.IDGenerator { return idGenerator{} }
