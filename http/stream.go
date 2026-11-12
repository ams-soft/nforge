package nhttp

import (
	"context"
	"errors"
	stdhttp "net/http"
	"sync"
	"sync/atomic"
	"time"
)

// streamExemptKey carries the releasable request deadlines of one request.
type streamExemptKey struct{}

// exemptions holds the deadline guards that apply to one request and that a
// long-lived response (Server-Sent Events) may release: the global handler
// timeout and the HTTP/3 stream timeout. Ordinary routes never release them.
type exemptions struct {
	mu       sync.Mutex
	releases []func()
}

func (e *exemptions) add(release func()) {
	e.mu.Lock()
	e.releases = append(e.releases, release)
	e.mu.Unlock()
}

func (e *exemptions) releaseAll() {
	e.mu.Lock()
	rs := e.releases
	e.releases = nil
	e.mu.Unlock()
	for _, r := range rs {
		r()
	}
}

// withExemptions returns ctx carrying the request's exemptions, creating them
// when an earlier middleware did not.
func withExemptions(ctx context.Context) (context.Context, *exemptions) {
	if e, ok := ctx.Value(streamExemptKey{}).(*exemptions); ok {
		return ctx, e
	}
	e := &exemptions{}
	return context.WithValue(ctx, streamExemptKey{}, e), e
}

// BeginStream prepares a long-lived response such as Server-Sent Events. It
// releases the deadlines nForge applies to ordinary requests, so the stream is
// not cut at 30 s: the global handler timeout (http.server.handler_timeout,
// when enabled), the HTTP/3 stream timeout (http.server.http3.max_stream_timeout)
// and the connection read and write deadlines (http.server.read_timeout and
// write_timeout). A route-level Timeout middleware that the application mounted
// itself is an explicit opt-in and is not released.
//
// The caller owns the write deadline from here on: it should set a short one
// before every write with http.NewResponseController(w).SetWriteDeadline, so a
// client that stops reading is dropped. Transport protections that do not bound
// the stream (read_header_timeout, header and body size limits) stay in force.
func BeginStream(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if e, ok := r.Context().Value(streamExemptKey{}).(*exemptions); ok {
		e.releaseAll()
	}
	rc := stdhttp.NewResponseController(w)
	// Unsupported on some wrappers: the error is not actionable, the caller's
	// per-write deadlines still apply.
	_ = rc.SetReadDeadline(time.Time{})
	_ = rc.SetWriteDeadline(time.Time{})
}

// Timeout returns a middleware that gives each request a deadline of d, for
// routes that opt in (http.server.handler_timeout is off by default). The
// deadline is not released by BeginStream, so mounting it on a streaming route
// is the explicit opt-in to bound that stream. d must be positive.
func Timeout(d time.Duration) func(stdhttp.Handler) stdhttp.Handler {
	if d <= 0 {
		panic("nhttp: Timeout requires a positive duration")
	}
	return func(next stdhttp.Handler) stdhttp.Handler {
		return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
			// Own timer, not context.WithTimeout: that one relies on the parent's
			// earlier deadline when there is one, which BeginStream may release.
			rd, cancel := newReleasableDeadline(r.Context(), d)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(rd))
		})
	}
}

// releasableDeadline is a context whose deadline can be released: until then
// it behaves as context.WithTimeout (Deadline, Err == DeadlineExceeded); after
// release the timer is stopped and the request is bound only by its parent.
type releasableDeadline struct {
	context.Context
	cancel   context.CancelCauseFunc
	deadline time.Time
	timer    *time.Timer
	released atomic.Bool
}

func newReleasableDeadline(parent context.Context, d time.Duration) (*releasableDeadline, context.CancelFunc) {
	inner, cancel := context.WithCancelCause(parent)
	c := &releasableDeadline{Context: inner, cancel: cancel, deadline: time.Now().Add(d)}
	c.timer = time.AfterFunc(d, func() { cancel(context.DeadlineExceeded) })
	return c, func() { c.timer.Stop(); cancel(context.Canceled) }
}

func (c *releasableDeadline) Deadline() (time.Time, bool) {
	if c.released.Load() {
		return c.Context.Deadline()
	}
	return c.deadline, true
}

func (c *releasableDeadline) Err() error {
	err := c.Context.Err()
	if err != nil && errors.Is(context.Cause(c.Context), context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return err
}

// release stops the timer; a deadline that already fired stays fired.
func (c *releasableDeadline) release() {
	if c.timer.Stop() {
		c.released.Store(true)
	}
}

// handlerTimeout gives every request context a deadline of d, so handlers,
// queries and loaders that honor ctx stop when it passes. It is applied only
// when http.server.handler_timeout is positive. Streams release it with
// BeginStream.
func handlerTimeout(d time.Duration) func(stdhttp.Handler) stdhttp.Handler {
	return func(next stdhttp.Handler) stdhttp.Handler {
		return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
			ctx, ex := withExemptions(r.Context())
			rd, cancel := newReleasableDeadline(ctx, d)
			defer cancel()
			ex.add(rd.release)
			next.ServeHTTP(w, r.WithContext(rd))
		})
	}
}
