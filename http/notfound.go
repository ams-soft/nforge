package nhttp

import (
	stdhttp "net/http"

	"github.com/ams-soft/nforge/codec"
)

// routeNotFound answers a path no route matches with the standard envelope, in
// the application's default codec, instead of net/http's text/plain body.
func routeNotFound(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	failEnvelope(w, r, stdhttp.StatusNotFound, "route not found")
}

// routeMethodNotAllowed answers a known path with another method. The router
// has already set the Allow header.
func routeMethodNotAllowed(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	failEnvelope(w, r, stdhttp.StatusMethodNotAllowed, "method not allowed")
}

func failEnvelope(w stdhttp.ResponseWriter, r *stdhttp.Request, status int, detail string) {
	format := codec.FromContext(r.Context()).DefaultFormat()
	if err := write(w, r, status, format, detail); err != nil {
		stdhttp.Error(w, stdhttp.StatusText(status), status)
	}
}
