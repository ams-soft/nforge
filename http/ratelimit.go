package nhttp

import (
	"context"
	"log/slog"
	"math"
	"net"
	stdhttp "net/http"
	"strconv"
	"sync"
	"time"

	"github.com/ams-soft/nforge/cache"
)

// RateLimitOption configures RateLimit.
type RateLimitOption func(*rateLimiter)

// RateLimitKey derives the client key of a request. Returning ok=false skips
// the limit for that request.
type RateLimitKey func(*stdhttp.Request) (key string, ok bool)

// KeyByRemoteIP limits per connection peer address. Behind a proxy or load
// balancer every client shares the proxy's address: use KeyByHeader with a
// header your trusted proxy overwrites, or KeyByPrincipal.
func KeyByRemoteIP(r *stdhttp.Request) (string, bool) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return "ip:" + host, host != ""
}

// KeyByPrincipal limits per authenticated subject (issuer and subject), and
// falls back to the peer address for unauthenticated requests. Mount it after
// Authenticate.
func KeyByPrincipal(r *stdhttp.Request) (string, bool) {
	if p, ok := PrincipalFromContext(r.Context()); ok && p.Subject != "" {
		return "sub:" + p.Issuer + "\x00" + p.Subject, true
	}
	return KeyByRemoteIP(r)
}

// KeyByHeader limits per the value of a request header, falling back to the
// peer address when it is absent. Use it ONLY for a header that a trusted
// proxy sets or overwrites (never one a client can choose): otherwise a client
// picks its own bucket and the limit means nothing.
func KeyByHeader(name string) RateLimitKey {
	return func(r *stdhttp.Request) (string, bool) {
		if v := r.Header.Get(name); v != "" && len(v) <= 256 {
			return "hdr:" + v, true
		}
		return KeyByRemoteIP(r)
	}
}

// WithRateLimitKey replaces the key function (default KeyByRemoteIP).
func WithRateLimitKey(f RateLimitKey) RateLimitOption {
	return func(l *rateLimiter) { l.key = f }
}

// WithRateLimitCache counts in a cache (Redis for a limit shared by every
// instance, or the in-process driver) instead of the middleware's own memory.
// The cache's tenant isolation applies when it is on.
func WithRateLimitCache(c *cache.Cache) RateLimitOption {
	return func(l *rateLimiter) { l.cache = c }
}

// WithRateLimitScope prefixes the keys, so several limits can share a cache.
func WithRateLimitScope(scope string) RateLimitOption {
	return func(l *rateLimiter) { l.scope = scope }
}

// WithRateLimitFailClosed answers 503 when the counter backend fails. The
// default is to let the request through (and log): a Redis outage should not
// take the API down with it.
func WithRateLimitFailClosed() RateLimitOption {
	return func(l *rateLimiter) { l.failClosed = true }
}

// WithRateLimitLogger sets the logger for backend failures (default slog.Default).
func WithRateLimitLogger(logger *slog.Logger) RateLimitOption {
	return func(l *rateLimiter) { l.logger = logger }
}

type rateLimiter struct {
	limit      int
	window     time.Duration
	key        RateLimitKey
	cache      *cache.Cache
	scope      string
	failClosed bool
	logger     *slog.Logger
	mem        *memCounter
}

// RateLimit returns a middleware that allows at most limit requests per
// window for each client key (a fixed window), answering 429 with
// Retry-After and the RateLimit-* headers beyond it. It panics at
// construction for a non-positive limit or window.
func RateLimit(limit int, window time.Duration, opts ...RateLimitOption) func(stdhttp.Handler) stdhttp.Handler {
	if limit < 1 || window < time.Second {
		panic("nhttp: RateLimit requires limit >= 1 and window >= 1s")
	}
	l := &rateLimiter{limit: limit, window: window, key: KeyByRemoteIP, logger: slog.Default()}
	for _, o := range opts {
		o(l)
	}
	if l.cache == nil {
		l.mem = newMemCounter()
	}
	return func(next stdhttp.Handler) stdhttp.Handler {
		return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
			key, ok := l.key(r)
			if !ok {
				next.ServeHTTP(w, r)
				return
			}
			count, reset, err := l.hit(r.Context(), l.scope+key)
			if err != nil {
				l.logger.WarnContext(r.Context(), "[HTTPModule] rate limit backend failed", "failClosed", l.failClosed)
				if l.failClosed {
					w.Header().Set("Retry-After", "1")
					failEnvelope(w, r, stdhttp.StatusServiceUnavailable, "rate limiting unavailable")
					return
				}
				next.ServeHTTP(w, r)
				return
			}
			remaining := max(int64(l.limit)-count, 0)
			secs := strconv.Itoa(int(math.Ceil(reset.Seconds())))
			h := w.Header()
			h.Set("RateLimit-Limit", strconv.Itoa(l.limit))
			h.Set("RateLimit-Remaining", strconv.FormatInt(remaining, 10))
			h.Set("RateLimit-Reset", secs)
			if count > int64(l.limit) {
				h.Set("Retry-After", secs)
				failEnvelope(w, r, stdhttp.StatusTooManyRequests, "too many requests")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// hit counts one request and returns the count in the window and the time left.
func (l *rateLimiter) hit(ctx context.Context, key string) (int64, time.Duration, error) {
	if l.cache == nil {
		n, reset := l.mem.hit(key, l.window)
		return n, reset, nil
	}
	n, err := l.cache.IncrementWithTTL(ctx, "ratelimit:"+key, 1, l.window)
	if err != nil {
		return 0, 0, err
	}
	exp, err := l.cache.TTL(ctx, "ratelimit:"+key)
	reset := l.window
	if err == nil && exp.Exists && !exp.Persistent {
		reset = max(exp.Remaining, time.Second)
	}
	return n, reset, nil
}

// memCounter is the in-process fixed-window counter. It is bounded: past
// maxRateKeys distinct keys it drops the expired windows, and if that is not
// enough it starts over, which can only grant a few extra requests, never deny.
type memCounter struct {
	mu sync.Mutex
	m  map[string]*memWindow
}

type memWindow struct {
	n   int64
	end time.Time
}

const maxRateKeys = 100_000

func newMemCounter() *memCounter { return &memCounter{m: map[string]*memWindow{}} }

func (c *memCounter) hit(key string, window time.Duration) (int64, time.Duration) {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	w := c.m[key]
	if w == nil || !now.Before(w.end) {
		if len(c.m) >= maxRateKeys {
			for k, v := range c.m {
				if !now.Before(v.end) {
					delete(c.m, k)
				}
			}
			if len(c.m) >= maxRateKeys {
				clear(c.m)
			}
		}
		w = &memWindow{end: now.Add(window)}
		c.m[key] = w
	}
	w.n++
	return w.n, max(w.end.Sub(now), time.Second)
}

// inflightLimit sheds load: at most n requests run at once, the rest get 503
// with Retry-After. A long-lived response that calls BeginStream gives its
// slot back, so streams do not starve ordinary requests.
func inflightLimit(n int) func(stdhttp.Handler) stdhttp.Handler {
	slots := make(chan struct{}, n)
	return func(next stdhttp.Handler) stdhttp.Handler {
		return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
			select {
			case slots <- struct{}{}:
			default:
				w.Header().Set("Retry-After", "1")
				failEnvelope(w, r, stdhttp.StatusServiceUnavailable, "server busy")
				return
			}
			var once sync.Once
			release := func() { once.Do(func() { <-slots }) }
			defer release()
			ctx, ex := withExemptions(r.Context())
			ex.add(release)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
