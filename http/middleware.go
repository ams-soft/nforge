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
func Tracing() func(stdhttp.Handler) stdhttp.Handler {
	return tracing.Middleware()
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
