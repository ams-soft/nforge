package message

import (
	"context"

	"github.com/ams-soft/nforge/telemetry"
	amqp "github.com/rabbitmq/amqp091-go"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// Trace context travels in reserved x-nforge-* headers, never in the caller's
// headers (user headers cannot start with x-), and is never used for
// authorization.
const traceHeaderPrefix = "x-nforge-"

var traceKeys = [...]string{"traceparent", "tracestate"}

// injectTrace copies the span context of ctx into headers (W3C trace context).
// Without a valid span in ctx it adds nothing.
func injectTrace(ctx context.Context, headers amqp.Table) {
	if !trace.SpanContextFromContext(ctx).IsValid() {
		return
	}
	carrier := propagation.MapCarrier{}
	telemetry.Propagator().Inject(ctx, carrier)
	for _, k := range traceKeys {
		if v := carrier.Get(k); v != "" {
			headers[traceHeaderPrefix+k] = v
		}
	}
}

// extractTrace returns the remote span context a publisher put in headers.
func extractTrace(headers amqp.Table) trace.SpanContext {
	carrier := propagation.MapCarrier{}
	for _, k := range traceKeys {
		if v, ok := headers[traceHeaderPrefix+k].(string); ok {
			carrier.Set(k, v)
		}
	}
	return trace.SpanContextFromContext(propagation.TraceContext{}.Extract(context.Background(), carrier))
}

// startPublishSpan opens a producer span when a tracer is configured.
func (c *Client) startPublishSpan(ctx context.Context, queue string) (context.Context, func(error)) {
	if c.tracer == nil {
		return ctx, func(error) {}
	}
	ctx, span := c.tracer.Start(ctx, queue+" publish", trace.WithSpanKind(trace.SpanKindProducer))
	return ctx, endSpan(span)
}

// consumerContext opens the span of one delivery. The producer is another
// process that may be outside nForge, so unless tracing.otel.trust_incoming is
// true its context is attached as a link, not as the parent: a producer cannot
// then pick the trace ID or the sampling decision of the consumer.
func (c *Client) consumerContext(ctx context.Context, queue string, headers amqp.Table) (context.Context, func(error)) {
	if c.tracer == nil {
		return ctx, func(error) {}
	}
	remote := extractTrace(headers)
	opts := []trace.SpanStartOption{trace.WithSpanKind(trace.SpanKindConsumer)}
	if remote.IsValid() {
		if c.trustIncoming {
			ctx = trace.ContextWithRemoteSpanContext(ctx, remote)
		} else {
			opts = append(opts, trace.WithLinks(trace.Link{SpanContext: remote}), trace.WithNewRoot())
		}
	}
	ctx, span := c.tracer.Start(ctx, queue+" process", opts...)
	return ctx, endSpan(span)
}

func endSpan(span trace.Span) func(error) {
	return func(err error) {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "message handling failed")
		}
		span.End()
	}
}
