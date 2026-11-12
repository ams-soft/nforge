//go:build integration

package message

import (
	"context"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

func sdktraceProvider(t testing.TB) *sdktrace.TracerProvider {
	t.Helper()
	tp := sdktrace.NewTracerProvider()
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	return tp
}

func traceIDOf(ctx context.Context) string {
	return trace.SpanContextFromContext(ctx).TraceID().String()
}
