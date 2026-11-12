package sse

import (
	"context"
	"errors"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	nhttp "github.com/ams-soft/nforge/http"
)

// IdentityResolver returns the user ID a request may stream for. It runs
// before any SSE header is written, on the server side only: it must never
// trust a user ID that the client chose (query, header, body). Return an error
// wrapping ErrUnauthorized (401) or ErrForbidden (403); any other error is a 401.
type IdentityResolver func(*http.Request) (string, error)

// HandlerOption configures Manager.Handler.
type HandlerOption func(*handlerOptions)

type handlerOptions struct {
	identity IdentityResolver
	expiry   func(*http.Request) time.Time
}

// WithExpiry sets when the authorization behind a request ends (the token's
// exp). The stream is closed no later than that instant, after a final
// "reauth" event. The default reads Principal.ExpiresAt of the validated
// token; a custom resolver should provide its own.
func WithExpiry(f func(*http.Request) time.Time) HandlerOption {
	return func(o *handlerOptions) { o.expiry = f }
}

// UserKey is the stream key of an authenticated principal: issuer, tenant and
// subject, each unambiguously delimited. Two tenants or issuers that happen to
// share a subject ID never share a stream. PublishToUser takes this key, so a
// publisher that holds the principal calls PublishToUser(ctx, sse.UserKey(p), …).
func UserKey(p nhttp.Principal) string { return UserKeyOf(p.Issuer, p.TenantID, p.Subject) }

// UserKeyOf builds the key from its parts, for publishers that know the
// issuer, tenant and subject of a user but hold no Principal.
func UserKeyOf(issuer, tenantID, subject string) string {
	return strconv.Itoa(len(issuer)) + ":" + issuer + "|" + strconv.Itoa(len(tenantID)) + ":" + tenantID + "|" + subject
}

// WithIdentityResolver replaces the default identity, which is the Subject of
// the principal that nhttp.Authenticate validated for the request.
func WithIdentityResolver(r IdentityResolver) HandlerOption {
	return func(o *handlerOptions) { o.identity = r }
}

// principalIdentity is the default resolver. It trusts only the principal that
// the JOSE bearer middleware stored in the request context after validating the
// token; with no principal (the route lacks nhttp.Authenticate, or the
// application forgot it) the stream is refused, never opened anonymously. The
// key is UserKey(principal), not the bare subject.
func principalIdentity(r *http.Request) (string, error) {
	p, ok := nhttp.PrincipalFromContext(r.Context())
	if !ok || p.Subject == "" {
		return "", ErrUnauthorized
	}
	return UserKey(p), nil
}

// principalExpiry is the default expiry: the validated token's exp.
func principalExpiry(r *http.Request) time.Time {
	if p, ok := nhttp.PrincipalFromContext(r.Context()); ok {
		return p.ExpiresAt
	}
	return time.Time{}
}

// Handler returns the stream endpoint. Register it in an application module's
// Init on a GET route of your choice, behind nhttp.Authenticate:
//
//	server.Router().With(nhttp.Authenticate(j, "access")).Get("/notifications/stream", sse.Handler())
//
// A request is refused before any SSE header with 401/403 (identity), 403
// (origin), 429 (rate or per-user limit), 503 (global limit, shutdown, not
// started yet) or 500 (the response chain cannot flush). Stream lifetime is not
// bound by the server's handler, read or write timeouts; every frame write has
// its own deadline (sse.write_timeout), and the stream ends after
// sse.max_lifetime so an expired or revoked token cannot keep it forever.
func (m *Manager) Handler(opts ...HandlerOption) http.Handler {
	o := handlerOptions{identity: principalIdentity, expiry: principalExpiry}
	for _, f := range opts {
		f(&o)
	}
	if o.identity == nil {
		panic("sse: WithIdentityResolver requires a non-nil resolver")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { m.serve(w, r, o) })
}

func refuse(w http.ResponseWriter, r *http.Request, status int, msg string, retryAfter time.Duration) {
	w.Header().Set("Cache-Control", "no-store")
	if retryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())))
	}
	_ = nhttp.SendJSON(w, r, status, msg)
}

func (m *Manager) serve(w http.ResponseWriter, r *http.Request, o handlerOptions) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		refuse(w, r, http.StatusMethodNotAllowed, "method not allowed", 0)
		return
	}
	if !m.originAllowed(r) {
		m.stats.rejected.Add(1)
		refuse(w, r, http.StatusForbidden, "origin not allowed", 0)
		return
	}
	user, err := o.identity(r)
	switch {
	case errors.Is(err, ErrForbidden):
		refuse(w, r, http.StatusForbidden, "forbidden", 0)
		return
	case err != nil:
		w.Header().Set("WWW-Authenticate", `Bearer realm="nforge-api"`)
		refuse(w, r, http.StatusUnauthorized, "authentication required", 0)
		return
	}
	if m.validUser(user) != nil {
		// an identity the module cannot carry is a server-side misconfiguration
		m.logger.ErrorContext(r.Context(), "[SSEModule] identity resolver returned an unusable user id")
		refuse(w, r, http.StatusForbidden, "forbidden", 0)
		return
	}
	if last := r.Header.Get("Last-Event-ID"); last != "" && (len(last) > m.cfg.Limits.MaxEventIDBytes || !validText(last)) {
		refuse(w, r, http.StatusBadRequest, "invalid Last-Event-ID", 0)
		return
	}
	if !canFlush(w) {
		m.logger.ErrorContext(r.Context(), "[SSEModule] the response chain cannot flush; streaming is unavailable")
		refuse(w, r, http.StatusInternalServerError, "streaming unsupported", 0)
		return
	}
	if !m.limiter.allow() {
		m.stats.rejected.Add(1)
		refuse(w, r, http.StatusTooManyRequests, "too many connection attempts", time.Second)
		return
	}

	c := newConn(user, m.cfg.BufferSize)
	switch err := m.register(c); {
	case errors.Is(err, errRejectClosed):
		m.stats.rejected.Add(1)
		refuse(w, r, http.StatusServiceUnavailable, "service unavailable", m.cfg.Retry)
		return
	case errors.Is(err, errRejectGlobal):
		m.stats.rejected.Add(1)
		refuse(w, r, http.StatusServiceUnavailable, "too many connections", m.cfg.Retry)
		return
	case errors.Is(err, errRejectUser):
		m.stats.rejected.Add(1)
		// the same answer whoever else is connected: no presence disclosure beyond the caller's own limit
		refuse(w, r, http.StatusTooManyRequests, "too many connections", m.cfg.Retry)
		return
	}
	defer m.unregister(c)
	m.stats.connections.Add(1)

	nhttp.BeginStream(w, r)
	m.capSendBuffer(r)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("X-Accel-Buffering", "no")
	if r.ProtoMajor == 1 {
		h.Set("Connection", "keep-alive") // forbidden on HTTP/2 and HTTP/3
	}
	w.WriteHeader(http.StatusOK)

	rc := http.NewResponseController(w)
	write := func(b []byte) error {
		_ = rc.SetWriteDeadline(time.Now().Add(m.cfg.WriteTimeout)) // unsupported on some writers: not actionable
		if _, err := w.Write(b); err != nil {
			return err
		}
		return rc.Flush()
	}
	// The first flush commits the headers promptly, so the browser fires "open".
	if err := write([]byte("retry: " + strconv.FormatInt(m.cfg.Retry.Milliseconds(), 10) + "\n\n: connected\n\n")); err != nil {
		m.stats.writeErrors.Add(1)
		return
	}

	heartbeat := time.NewTicker(m.cfg.Heartbeat)
	defer heartbeat.Stop()
	life := m.cfg.MaxLifetime
	life += time.Duration(rand.Int64N(int64(life)/10 + 1)) //nolint:gosec // jitter that spreads reconnections, not a security value
	reason := "lifetime"
	if exp := o.expiry(r); !exp.IsZero() {
		if left := max(time.Until(exp), 0); left < life {
			life, reason = left, "expired" // the token ends before max_lifetime does
		}
	}
	lifetime := time.NewTimer(life)
	defer lifetime.Stop()

	for {
		select {
		case frame := <-c.ch:
			if err := write(frame); err != nil {
				m.stats.writeErrors.Add(1)
				return
			}
		case <-heartbeat.C:
			if err := write([]byte(": heartbeat\n\n")); err != nil {
				m.stats.heartbeatErrors.Add(1)
				return
			}
		case <-c.done:
			return // slow client: its queue overflowed; the stream is not continued with a gap
		case <-lifetime.C:
			m.stats.expiredLifetime.Add(1)
			// Tell the page why, so it can refresh its token and reconnect instead of
			// retrying with a credential that is about to be refused for good.
			_ = write([]byte("event: reauth\ndata: " + reason + "\n\n"))
			return
		case <-m.closing:
			return
		case <-r.Context().Done():
			return
		}
	}
}

// canFlush reports, without side effects, whether a Flush can reach the
// connection through the writer wrappers (http.Flusher, FlushError or Unwrap).
func canFlush(w http.ResponseWriter) bool {
	for range 16 {
		switch v := w.(type) {
		case http.Flusher:
			return true
		case interface{ FlushError() error }:
			return true
		case interface{ Unwrap() http.ResponseWriter }:
			w = v.Unwrap()
		default:
			return false
		}
	}
	return false
}

// originAllowed applies the CSRF-style origin rule: requests without an Origin
// header (same-origin GETs, non-browser clients) pass; with one, it must be an
// allowed origin, or the request's own origin when none is configured.
func (m *Manager) originAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	if len(m.cfg.AllowedOrigins) > 0 {
		return slices.Contains(m.cfg.AllowedOrigins, origin)
	}
	u, err := url.Parse(origin)
	return err == nil && u.Host != "" && strings.EqualFold(u.Host, r.Host)
}

var (
	errRejectClosed = errors.New("sse: not accepting")
	errRejectGlobal = errors.New("sse: global limit")
	errRejectUser   = errors.New("sse: user limit")
)

// connKey carries the accepted connection through the request context.
type connKey struct{}

// ConnContext is for http.Server.ConnContext: it remembers the connection so a
// stream can cap its kernel send buffer (sse.send_buffer_bytes). The module
// installs it on the HTTP server; a test server can set it itself.
func ConnContext(next func(context.Context, net.Conn) context.Context) func(context.Context, net.Conn) context.Context {
	return func(ctx context.Context, c net.Conn) context.Context {
		if next != nil {
			ctx = next(ctx, c)
		}
		return context.WithValue(ctx, connKey{}, c)
	}
}

// capSendBuffer applies sse.send_buffer_bytes to the TCP connection of an
// HTTP/1.1 stream. Failures are not actionable: the stream still works, it is
// just protected by the larger default buffer.
func (m *Manager) capSendBuffer(r *http.Request) {
	if m.cfg.SendBufferBytes <= 0 || r.ProtoMajor != 1 {
		return
	}
	c, _ := r.Context().Value(connKey{}).(net.Conn)
	for range 4 { // unwrap TLS and similar wrappers
		switch v := c.(type) {
		case *net.TCPConn:
			_ = v.SetWriteBuffer(m.cfg.SendBufferBytes)
			return
		case interface{ NetConn() net.Conn }:
			c = v.NetConn()
		default:
			return
		}
	}
}
