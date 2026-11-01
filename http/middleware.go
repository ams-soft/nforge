package nhttp

import (
	"log/slog"
	stdhttp "net/http"

	"github.com/ams-soft/nforge/http/internal/accesslog"
	"github.com/ams-soft/nforge/http/internal/tracing"
)

// Tracing opens one span per request and guarantees request_id = trace_id:
//   - traceparent received (call from another service): continues that trace;
//   - X-Request-Id received in trace_id format: it becomes the trace_id;
//   - neither: a new trace_id, returned in the response's X-Request-Id.
//
// It must be the first middleware (it replaces the router's middleware.RequestID),
// so that the following ones and the logs already see the span and the id in the ctx.
//
// By default the caller's trace context is trusted. WithoutIncomingContext makes
// every request start its own trace (use it on a public edge); the server
// applies it when tracing.otel.trust_incoming is false.
func Tracing(opts ...TracingOption) func(stdhttp.Handler) stdhttp.Handler {
	o := tracing.Options{TrustIncoming: true}
	for _, opt := range opts {
		opt(&o)
	}
	return tracing.Middleware(o)
}

// TracingOption configures Tracing.
type TracingOption func(*tracing.Options)

// WithoutIncomingContext ignores the traceparent, tracestate, baggage and
// X-Request-Id of the request: it always starts a new trace with a new id.
func WithoutIncomingContext() TracingOption {
	return func(o *tracing.Options) { o.TrustIncoming = false }
}

// Logger logs one "request" record per request with method, path, pattern
// (the matched route, e.g. "/users/{id}"), status, bytes, duration and remote,
// at INFO, WARN (4xx) or ERROR (5xx). It is preferred over the router's
// middleware.SlogLogger, which always logs at INFO (dropping failures under the
// stg/prd levels) and adds request_id itself, duplicating the one the logs
// context handler injects from the request ctx.
func Logger(logger *slog.Logger) func(stdhttp.Handler) stdhttp.Handler {
	return accesslog.Middleware(logger)
}
