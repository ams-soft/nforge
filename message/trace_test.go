package message

import (
	"context"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func tracerFor(t *testing.T) (*Client, *tracetest.SpanRecorder) {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	return &Client{tracer: tp.Tracer("test")}, rec
}

func TestTraceContextRoundTripsThroughHeaders(t *testing.T) {
	c, _ := tracerFor(t)
	ctx, endPub := c.startPublishSpan(context.Background(), "q")
	headers := amqp.Table{"tenant": "a"}
	injectTrace(ctx, headers)
	endPub(nil)
	if _, ok := headers["x-nforge-traceparent"].(string); !ok {
		t.Fatalf("headers = %v, want x-nforge-traceparent", headers)
	}
	if headers["tenant"] != "a" {
		t.Error("caller headers must be untouched")
	}
	got := extractTrace(headers)
	if !got.IsValid() || got.TraceID() != trace.SpanContextFromContext(ctx).TraceID() {
		t.Fatalf("extracted %v, want the publisher's trace", got)
	}
}

func TestUntracedPublishAddsNoHeaders(t *testing.T) {
	h := amqp.Table{}
	injectTrace(context.Background(), h)
	if len(h) != 0 {
		t.Fatalf("headers = %v, want none without a span", h)
	}
	if extractTrace(amqp.Table{"x-nforge-traceparent": "garbage"}).IsValid() {
		t.Error("a malformed traceparent must be ignored")
	}
}

func TestConsumerParentOnlyWhenIncomingTraceIsTrusted(t *testing.T) {
	c, rec := tracerFor(t)
	pubCtx, end := c.startPublishSpan(context.Background(), "q")
	headers := amqp.Table{}
	injectTrace(pubCtx, headers)
	end(nil)
	want := trace.SpanContextFromContext(pubCtx).TraceID()

	// untrusted: a new trace that LINKS to the producer (it cannot pick the trace ID or sampling)
	hctx, endC := c.consumerContext(context.Background(), "q", headers)
	endC(nil)
	if got := trace.SpanContextFromContext(hctx).TraceID(); got == want {
		t.Error("untrusted producer context must not become the parent")
	}
	spans := rec.Ended()
	last := spans[len(spans)-1]
	if len(last.Links()) != 1 || last.Links()[0].SpanContext.TraceID() != want {
		t.Errorf("links = %v, want one link to the producer", last.Links())
	}

	// trusted: the consumer continues the producer's trace
	c.trustIncoming = true
	hctx, endC = c.consumerContext(context.Background(), "q", headers)
	endC(nil)
	if got := trace.SpanContextFromContext(hctx).TraceID(); got != want {
		t.Errorf("trusted: trace = %v, want %v", got, want)
	}
}

func TestNoTracerNoSpans(t *testing.T) {
	c := &Client{}
	ctx, end := c.consumerContext(context.Background(), "q", amqp.Table{})
	end(nil)
	if trace.SpanContextFromContext(ctx).IsValid() {
		t.Error("no tracer: no span")
	}
}
