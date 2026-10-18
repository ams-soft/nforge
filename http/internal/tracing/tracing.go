// Package tracing implements the nhttp Tracing middleware: one span per
// request with request_id = trace_id.
package tracing

import (
	stdhttp "net/http"

	router "github.com/ams-soft/go-ams-router"
	"github.com/ams-soft/go-ams-router/middleware"
	"github.com/ams-soft/nforge/telemetry"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// Middleware opens one span per request and guarantees request_id = trace_id:
//   - traceparent received (call from another service): continues that trace;
//   - X-Request-Id received in trace_id format: it becomes the trace_id;
//   - neither: a new trace_id, returned in the response's X-Request-Id.
//
// It must be the first middleware (it replaces the router's middleware.RequestID),
// so that the following ones and the logs already see the span and the id in the ctx.
func Middleware() func(stdhttp.Handler) stdhttp.Handler {
	otelMw := otelhttp.NewMiddleware("http.server",
		otelhttp.WithSpanNameFormatter(func(_ string, r *stdhttp.Request) string {
			return r.Method
		}),
	)

	return func(next stdhttp.Handler) stdhttp.Handler {
		return traceIDFromRequestID(otelMw(requestIDFromTrace(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
			next.ServeHTTP(w, r)

			// The router creates the route context before the root middlewares and
			// fills it while routing, so the full pattern ("/users/{id}", across
			// sub-routers) is readable here; it keeps the span name low-cardinality.
			rctx := router.RouteContext(r.Context())
			if rctx == nil {
				return // mounted outside the router
			}
			if pattern := rctx.RoutePattern(); pattern != "" {
				span := trace.SpanFromContext(r.Context())
				span.SetName(r.Method + " " + pattern)
				span.SetAttributes(attribute.String("http.route", pattern))
			}
		}))))
	}
}

// traceIDFromRequestID runs BEFORE the span is created: if the received
// X-Request-Id looks like a trace_id (32 hex chars or a UUID), it becomes the
// trace_id of the request's root span.
func traceIDFromRequestID(next stdhttp.Handler) stdhttp.Handler {
	return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		if tid, ok := telemetry.ParseTraceID(r.Header.Get(middleware.RequestIDHeader)); ok {
			r = r.WithContext(telemetry.ContextWithTraceID(r.Context(), tid))
		}
		next.ServeHTTP(w, r)
	})
}

// requestIDFromTrace runs AFTER the span is created and sets request_id = trace_id,
// reusing the router's middleware.RequestID (response header and
// middleware.GetRequestID, used by the logger). A received X-Request-Id that
// did not become the trace_id (invalid format or traceparent with another
// trace) is kept on the span as request_id.original when it has a valid
// request-ID shape.
func requestIDFromTrace(next stdhttp.Handler) stdhttp.Handler {
	withID := middleware.RequestID(next)

	return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		// With tracing disabled there is no trace_id: the router uses the header or generates a UUIDv7.
		if sc := trace.SpanContextFromContext(r.Context()); sc.HasTraceID() {
			id := sc.TraceID().String()
			if orig := r.Header.Get(middleware.RequestIDHeader); orig != "" {
				// Same acceptance rule as the router's RequestID: anything else is
				// client noise and is not copied into the span.
				if tid, ok := telemetry.ParseTraceID(orig); (!ok || tid != sc.TraceID()) && validRequestID(orig) {
					trace.SpanFromContext(r.Context()).SetAttributes(attribute.String("request_id.original", orig))
				}
			}
			r.Header.Set(middleware.RequestIDHeader, id)
			trace.SpanFromContext(r.Context()).SetAttributes(attribute.String("request_id", id))
		}
		withID.ServeHTTP(w, r)
	})
}

// validRequestID mirrors go-ams-router's middleware.RequestID rule (not
// exported): at most 64 characters in [A-Za-z0-9._-].
func validRequestID(id string) bool {
	if len(id) > 64 {
		return false
	}
	for i := range len(id) {
		switch c := id[i]; {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}
