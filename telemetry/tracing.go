package telemetry

import (
	"context"
	"strings"

	"github.com/ams-soft/nforge/module"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// Propagator returns the W3C propagators the framework uses.
func Propagator() propagation.TextMapPropagator {
	return propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{})
}

// NewTracerProvider builds the provider of the tracing settings and touches no
// global state.
func NewTracerProvider(ctx context.Context, s Settings, app module.App) (*sdktrace.TracerProvider, error) {
	cfg := s.Otel
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

	res, err := NewResource(ctx, app)
	if err != nil {
		return nil, err
	}

	return sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithBatcher(exporter),
		sdktrace.WithIDGenerator(IDGenerator()), // trace_id taken from X-Request-Id
		sdktrace.WithSampler(newSampler(cfg.Ratio())),
	), nil
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
