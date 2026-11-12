package nhttp

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	stdhttp "net/http"
	"reflect"
)

// Empty marks a handler without a request or a response body.
type Empty struct{}

// Handler is a typed route handler. req is already decoded and validated (the
// query string on Get, the body otherwise). The returned value goes through the
// same conforming and validation as any typed route. r gives the context, the
// path parameters and the headers.
type Handler[Req, Res any] func(r *stdhttp.Request, req Req) (Res, error)

// StatusError is an error with a response. Its message reaches the client, so it
// must be safe to show; any other error from a Handler is a 500 with a generic
// message and the real error goes to the log.
type StatusError struct {
	Status  int
	Message string
}

func (e *StatusError) Error() string { return e.Message }

// Error returns a *StatusError. status must be 400 or above.
func Error(status int, message string) error {
	return &StatusError{Status: status, Message: message}
}

// Get registers a GET route. Unlike TGet the compiler checks the types: Req is
// the query string (a struct, or Empty) and Res the response.
func Get[Req, Res any](tr *TypedRouter, pattern string, h Handler[Req, Res], opts ...Opts) {
	route(tr, stdhttp.MethodGet, pattern, h, opts)
}

// Post registers a POST route. Req is the body (or Empty) and Res the response
// (or Empty, which answers 204). Opts.Status sets the success status.
func Post[Req, Res any](tr *TypedRouter, pattern string, h Handler[Req, Res], opts ...Opts) {
	route(tr, stdhttp.MethodPost, pattern, h, opts)
}

// Put is Post for PUT.
func Put[Req, Res any](tr *TypedRouter, pattern string, h Handler[Req, Res], opts ...Opts) {
	route(tr, stdhttp.MethodPut, pattern, h, opts)
}

// Patch is Post for PATCH.
func Patch[Req, Res any](tr *TypedRouter, pattern string, h Handler[Req, Res], opts ...Opts) {
	route(tr, stdhttp.MethodPatch, pattern, h, opts)
}

// Delete is Post for DELETE.
func Delete[Req, Res any](tr *TypedRouter, pattern string, h Handler[Req, Res], opts ...Opts) {
	route(tr, stdhttp.MethodDelete, pattern, h, opts)
}

// sample is the value handle takes for T: nil for Empty, otherwise its zero.
func sample[T any]() any {
	if reflect.TypeFor[T]() == reflect.TypeFor[Empty]() {
		return nil
	}
	var zero T
	return zero
}

func route[Req, Res any](tr *TypedRouter, method, pattern string, h Handler[Req, Res], opts []Opts) {
	var o Opts
	if len(opts) > 0 {
		o = opts[0]
	}
	if o.Status != 0 && (o.Status < stdhttp.StatusOK || o.Status >= stdhttp.StatusMultipleChoices) {
		panic(fmt.Sprintf("nhttp: %s %s: Opts.Status must be 2xx, got %d", method, pattern, o.Status))
	}
	request, response := sample[Req](), sample[Res]()
	logger := tr.srv.logger
	tr.handle(method, pattern, request, response, func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		var req Req
		if request != nil {
			req = Body[Req](r)
		}
		res, err := h(r, req)
		if err != nil {
			var se *StatusError
			if errors.As(err, &se) && se.Status >= stdhttp.StatusBadRequest {
				_ = SendJSON(w, r, se.Status, se.Message)
				return
			}
			if contextFailure(w, r, err, logger) {
				return
			}
			logger.ErrorContext(r.Context(), "handler failed", "error", err)
			_ = SendJSON(w, r, stdhttp.StatusInternalServerError, "internal error")
			return
		}
		if reflect.TypeFor[Res]() == reflect.TypeFor[Empty]() {
			w.WriteHeader(cmp.Or(o.Status, stdhttp.StatusNoContent))
			return
		}
		_ = SendJSON(w, r, cmp.Or(o.Status, stdhttp.StatusOK), res)
	}, opts)
}

// StatusClientClosedRequest is the non-standard 499 (nginx's convention) the
// access log records when the client went away before the handler answered. No
// body is written: nobody is listening.
const StatusClientClosedRequest = 499

// contextFailure answers a handler error that is a context error and reports
// whether it did. These are not server faults: a deadline (the server's own
// handler_timeout, a route Timeout, a downstream call) is a 504 logged as a
// warning, and a client that disconnected is a 499 at debug level, so client
// aborts, which are everyday traffic, neither raise ERROR logs nor fake 500s.
func contextFailure(w stdhttp.ResponseWriter, r *stdhttp.Request, err error, logger *slog.Logger) bool {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		logger.WarnContext(r.Context(), "request deadline exceeded", "error", err)
		_ = SendJSON(w, r, stdhttp.StatusGatewayTimeout, "request timed out")
		return true
	case errors.Is(err, context.Canceled) && r.Context().Err() != nil:
		logger.DebugContext(r.Context(), "client closed the request")
		_ = SendJSON(w, r, StatusClientClosedRequest, "client closed request")
		return true
	}
	return false
}
