package nhttp

import (
	"context"
	stdhttp "net/http"
	"sync"
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
			dc, cancel := newDeadlineCtx(r.Context(), d)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(dc))
		})
	}
}

// deadlineCtx is a context with a deadline that can be released. Until then it
// behaves exactly like context.WithTimeout: Deadline reports the instant, Err
// is context.DeadlineExceeded after it, and contexts derived from it (every
// database driver and HTTP client derives one) observe DeadlineExceeded too.
//
// It deliberately does not embed a stdlib cancel context. Children of an
// embedding wrapper find the embedded cancelCtx through Value and register on
// it directly, so they would see context.Canceled and never call Err here.
// With its own Done channel they take the generic propagation path and read
// Err of this context. After release the timer is stopped and the request is
// bound only by its parent.
type deadlineCtx struct {
	parent   context.Context
	done     chan struct{}
	deadline time.Time

	mu       sync.Mutex
	err      error
	released bool
	timer    *time.Timer
	unhook   func() bool
}

func newDeadlineCtx(parent context.Context, d time.Duration) (*deadlineCtx, context.CancelFunc) {
	c := &deadlineCtx{parent: parent, done: make(chan struct{}), deadline: time.Now().Add(d)}
	if err := parent.Err(); err != nil {
		c.err = err
		close(c.done)
		return c, func() {}
	}
	c.mu.Lock()
	c.timer = time.AfterFunc(d, func() { c.finish(context.DeadlineExceeded) })
	c.unhook = context.AfterFunc(parent, func() { c.finish(parent.Err()) })
	c.mu.Unlock()
	return c, func() { c.finish(context.Canceled) }
}

// finish ends the context once, with the first error that arrives.
func (c *deadlineCtx) finish(err error) {
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return
	}
	c.err = err
	close(c.done)
	timer, unhook := c.timer, c.unhook
	c.mu.Unlock()
	if timer != nil {
		timer.Stop()
	}
	if unhook != nil {
		unhook()
	}
}

func (c *deadlineCtx) Deadline() (time.Time, bool) {
	c.mu.Lock()
	released := c.released
	c.mu.Unlock()
	pd, pok := c.parent.Deadline()
	switch {
	case released:
		return pd, pok
	case pok && pd.Before(c.deadline):
		return pd, true
	}
	return c.deadline, true
}

func (c *deadlineCtx) Done() <-chan struct{} { return c.done }

func (c *deadlineCtx) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *deadlineCtx) Value(key any) any { return c.parent.Value(key) }

// release stops the timer; a deadline that already fired stays fired.
func (c *deadlineCtx) release() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err == nil && c.timer != nil && c.timer.Stop() {
		c.released = true
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
			dc, cancel := newDeadlineCtx(ctx, d)
			defer cancel()
			ex.add(dc.release)
			next.ServeHTTP(w, r.WithContext(dc))
		})
	}
}
