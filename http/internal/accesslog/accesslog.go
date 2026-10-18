// Package accesslog implements the nhttp Logger middleware: one slog record
// per request.
package accesslog

import (
	"log/slog"
	stdhttp "net/http"
	"time"

	router "github.com/ams-soft/go-ams-router"
)

// Middleware logs the end of each request with method, path, route pattern,
// status, bytes, duration and remote address. Unlike the router's
// middleware.SlogLogger it picks the level from the status (INFO, WARN on 4xx,
// ERROR on 5xx), so failures survive the stg/prd level filters, and it leaves
// request_id to the logs context handler instead of adding a second copy.
func Middleware(logger *slog.Logger) func(stdhttp.Handler) stdhttp.Handler {
	return func(next stdhttp.Handler) stdhttp.Handler {
		return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w, status: stdhttp.StatusOK}

			next.ServeHTTP(sw, r)

			level := slog.LevelInfo
			switch {
			case sw.status >= 500:
				level = slog.LevelError
			case sw.status >= 400:
				level = slog.LevelWarn
			}

			// The router creates the route context before the root middlewares, so
			// the full matched pattern is here after next ("" when nothing matched).
			var pattern string
			if rctx := router.RouteContext(r.Context()); rctx != nil {
				pattern = rctx.RoutePattern()
			}

			logger.LogAttrs(r.Context(), level, "request",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.String("pattern", pattern),
				slog.Int("status", sw.status),
				slog.Int("bytes", sw.bytes),
				slog.Duration("duration", time.Since(start)),
				slog.String("remote", r.RemoteAddr),
			)
		})
	}
}

// statusWriter captures the status and bytes written (the router's one is not exported).
type statusWriter struct {
	stdhttp.ResponseWriter
	status      int
	bytes       int
	wroteHeader bool
}

func (sw *statusWriter) WriteHeader(code int) {
	if !sw.wroteHeader {
		sw.status = code
		sw.wroteHeader = true
	}
	sw.ResponseWriter.WriteHeader(code)
}

func (sw *statusWriter) Write(b []byte) (int, error) {
	if !sw.wroteHeader {
		sw.WriteHeader(stdhttp.StatusOK)
	}
	n, err := sw.ResponseWriter.Write(b)
	sw.bytes += n
	return n, err
}

// Unwrap lets http.ResponseController reach Flush/Hijack on the original writer.
func (sw *statusWriter) Unwrap() stdhttp.ResponseWriter {
	return sw.ResponseWriter
}
