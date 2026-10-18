package telemetry

import (
	"context"
	"encoding/binary"
	"math/rand/v2"
	"strings"

	"go.opentelemetry.io/otel/trace"
)

type traceIDKey struct{}

// ContextWithTraceID sets the trace_id that the next root span created with
// this ctx will use. It has no effect if the span has a parent (e.g. a received traceparent).
func ContextWithTraceID(ctx context.Context, id trace.TraceID) context.Context {
	return context.WithValue(ctx, traceIDKey{}, id)
}

// ParseTraceID accepts a 32-character hex id, with or without hyphens (UUID),
// and converts it into a trace_id. Ids in another format or all zeros are rejected.
func ParseTraceID(s string) (trace.TraceID, bool) {
	id, err := trace.TraceIDFromHex(strings.ToLower(strings.ReplaceAll(s, "-", "")))
	return id, err == nil
}

// idGenerator uses the trace_id stored in the ctx (ContextWithTraceID) for root
// spans; without it, it generates random ids like the SDK's default generator.
type idGenerator struct{}

func (idGenerator) NewIDs(ctx context.Context) (trace.TraceID, trace.SpanID) {
	tid, ok := ctx.Value(traceIDKey{}).(trace.TraceID)
	for !ok || !tid.IsValid() {
		binary.BigEndian.PutUint64(tid[:8], rand.Uint64()) //nolint:gosec // trace IDs need uniqueness, not secrecy (like the OTel SDK generator)
		binary.BigEndian.PutUint64(tid[8:], rand.Uint64()) //nolint:gosec // trace IDs need uniqueness, not secrecy (like the OTel SDK generator)
		ok = true
	}
	return tid, newSpanID()
}

func (idGenerator) NewSpanID(context.Context, trace.TraceID) trace.SpanID {
	return newSpanID()
}

func newSpanID() trace.SpanID {
	var sid trace.SpanID
	for !sid.IsValid() {
		binary.BigEndian.PutUint64(sid[:], rand.Uint64()) //nolint:gosec // trace IDs need uniqueness, not secrecy (like the OTel SDK generator)
	}
	return sid
}
