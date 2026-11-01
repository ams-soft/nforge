package nhttp

import (
	"context"
	stdhttp "net/http"
	"time"
)

// handlerTimeout gives every request context a deadline, so handlers, queries
// and loaders that honor ctx stop when it passes. The server's write timeout
// closes the connection but leaves a handler that ignores it running (and its
// pooled database connection busy).
func handlerTimeout(d time.Duration) func(stdhttp.Handler) stdhttp.Handler {
	return func(next stdhttp.Handler) stdhttp.Handler {
		return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// bodyLimit bounds every request body, including those that raw routes read
// from r.Body directly (typed routes and Parse* are bound by the codec limit
// as well). One byte more than max is allowed through so the codec's own
// limit, which owns the 413 for typed routes, still sees the excess first.
func bodyLimit(max int64) func(stdhttp.Handler) stdhttp.Handler {
	return func(next stdhttp.Handler) stdhttp.Handler {
		return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
			if r.Body != nil && r.Body != stdhttp.NoBody {
				r.Body = stdhttp.MaxBytesReader(w, r.Body, max+1)
			}
			next.ServeHTTP(w, r)
		})
	}
}
