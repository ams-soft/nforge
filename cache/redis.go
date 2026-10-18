package cache

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"os"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/redis/go-redis/v9/maintnotifications"
)

// maxCAFileBytes bounds the PEM file of cache.redis.tls.ca_file.
const maxCAFileBytes = 1 << 20

// redisBackend adapts go-redis. The client's own command retries are
// disabled (MaxRetries: -1): pure reads are retried here, within the
// operation deadline; mutations are never reissued, because after a lost
// reply they may already have been applied. Broken connections are simply
// replaced by the pool on later operations, so the same client recovers
// when Redis comes back.
type redisBackend struct {
	client     *redis.Client
	opTimeout  time.Duration
	retries    int
	minBackoff time.Duration
	maxBackoff time.Duration
	maxValue   int64
}

func newRedis(cfg Config) (*redisBackend, error) {
	r := cfg.Redis
	ep, err := r.endpoint()
	if err != nil {
		return nil, err
	}
	opts := &redis.Options{
		Addr:     ep.addr(),
		Username: ep.username,
		Password: ep.password,
		DB:       ep.database,

		MaxRetries:            -1, // no automatic replay; see redisBackend
		DialerRetries:         1,  // one dial attempt per operation; the next operation dials again
		ContextTimeoutEnabled: true,
		DialTimeout:           r.Timeouts.Dial,
		ReadTimeout:           r.Timeouts.Read,
		WriteTimeout:          r.Timeouts.Write,
		PoolTimeout:           r.Timeouts.Pool,

		PoolSize:        r.Pool.Size,
		MinIdleConns:    r.Pool.MinIdleConns,
		MaxIdleConns:    r.Pool.MaxIdleConns,
		MaxActiveConns:  r.Pool.MaxActiveConns,
		ConnMaxIdleTime: r.Pool.MaxConnIdleTime,
		ConnMaxLifetime: r.Pool.MaxConnLifetime,

		DisableIdentity:          true, // no CLIENT SETINFO on connect
		MaintNotificationsConfig: &maintnotifications.Config{Mode: maintnotifications.ModeDisabled},
	}
	if ep.tls {
		tc, err := redisTLS(r.TLS, ep.host)
		if err != nil {
			return nil, err
		}
		opts.TLSConfig = tc
	}
	return &redisBackend{
		client:     redis.NewClient(opts),
		opTimeout:  r.Timeouts.Operation,
		retries:    *r.Retry.ReadRetries,
		minBackoff: r.Retry.MinBackoff,
		maxBackoff: r.Retry.MaxBackoff,
		maxValue:   int64(cfg.Limits.MaxValueBytes),
	}, nil
}

// redisTLS verifies the server name (default: the host) with TLS 1.2+; the
// CA file is read only here, when TLS is actually selected.
func redisTLS(c RedisTLSConfig, host string) (*tls.Config, error) {
	tc := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: c.ServerName}
	if tc.ServerName == "" {
		tc.ServerName = host
	}
	if c.CAFile == "" {
		return tc, nil
	}
	f, err := os.Open(c.CAFile)
	if err != nil {
		return nil, invalid("cache.redis.tls.ca_file cannot be read")
	}
	defer func() { _ = f.Close() }()
	pem, err := io.ReadAll(io.LimitReader(f, maxCAFileBytes+1))
	if err != nil || len(pem) > maxCAFileBytes {
		return nil, invalid("cache.redis.tls.ca_file cannot be read or is too large")
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, invalid("cache.redis.tls.ca_file has no PEM certificate")
	}
	tc.RootCAs = pool
	return tc, nil
}

func (b *redisBackend) name() string { return DriverRedis }

// opContext bounds an operation by cache.redis.timeouts.operation; an
// earlier caller deadline wins.
func (b *redisBackend) opContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, b.opTimeout)
}

// read runs a pure read with bounded, jittered retries on transport
// failures, all inside the operation deadline.
func (b *redisBackend) read(ctx context.Context, fn func(context.Context) error) error {
	ctx, cancel := b.opContext(ctx)
	defer cancel()
	backoff := b.minBackoff
	for attempt := 0; ; attempt++ {
		err := fn(ctx)
		if err == nil {
			return nil
		}
		cerr := classify(ctx, err, false)
		if attempt >= b.retries || !retryable(cerr) || ctx.Err() != nil {
			return cerr
		}
		wait := backoff/2 + rand.N(backoff/2+1)
		if dl, ok := ctx.Deadline(); ok && time.Until(dl) <= wait {
			return cerr // no time left for another attempt
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return cerr
		case <-t.C:
		}
		backoff = min(backoff*2, b.maxBackoff)
	}
}

// write runs a mutation exactly once.
func (b *redisBackend) write(ctx context.Context, fn func(context.Context) error) error {
	ctx, cancel := b.opContext(ctx)
	defer cancel()
	if err := fn(ctx); err != nil {
		return classify(ctx, err, true)
	}
	return nil
}

func retryable(err error) bool {
	var ce *Error
	return errors.As(err, &ce) && ce.Kind == ErrUnavailable && ce.Reason != "auth" && ce.Reason != "server"
}

// classify converts a go-redis error into a sanitized cache error. The
// Redis text is never kept: it may echo credentials or addresses.
//
//   - server replies (redis.Error) are authoritative: the command was
//     processed or rejected, never "unknown";
//   - failures before the command left the client (pool timeout, dial
//     error, closed client) are ErrUnavailable;
//   - anything else after submission (read timeout, EOF, cancellation) is
//     ErrOutcomeUnknown for mutations.
func classify(ctx context.Context, err error, mutation bool) error {
	if errors.Is(err, redis.Nil) {
		return newError(ErrNotFound, "")
	}
	var rerr redis.Error
	if errors.As(err, &rerr) {
		msg := rerr.Error()
		switch {
		case strings.HasPrefix(msg, "NOAUTH"), strings.HasPrefix(msg, "WRONGPASS"), strings.HasPrefix(msg, "NOPERM"):
			return newError(ErrUnavailable, "auth")
		case strings.Contains(msg, "not an integer"):
			return newError(ErrNotInteger, "")
		case strings.Contains(msg, "would overflow"):
			return newError(ErrCounterOverflow, "")
		case strings.Contains(msg, "invalid expire time"):
			return newError(ErrInvalidTTL, "out_of_range")
		case strings.HasPrefix(msg, "WRONGTYPE"):
			return newError(ErrNotInteger, "wrong_type")
		default:
			return newError(ErrUnavailable, "server")
		}
	}
	unsent := ""
	var opErr *net.OpError
	switch {
	case errors.Is(err, redis.ErrPoolTimeout), errors.Is(err, redis.ErrPoolExhausted):
		unsent = "pool_timeout"
	case errors.Is(err, redis.ErrClosed):
		unsent = "closed"
	case errors.As(err, &opErr) && opErr.Op == "dial":
		unsent = "connect"
	}
	var cause error
	if cerr := ctx.Err(); cerr != nil {
		cause = cerr
	}
	if unsent != "" {
		return &Error{Kind: ErrUnavailable, Reason: unsent, cause: cause}
	}
	reason := "connection"
	var ne net.Error
	if cause != nil || (errors.As(err, &ne) && ne.Timeout()) {
		reason = "timeout"
	}
	if mutation {
		return &Error{Kind: ErrOutcomeUnknown, Reason: reason, also: []error{ErrUnavailable}, cause: cause}
	}
	return &Error{Kind: ErrUnavailable, Reason: reason, cause: cause}
}

func (b *redisBackend) get(ctx context.Context, key string) ([]byte, error) {
	var out []byte
	err := b.read(ctx, func(ctx context.Context) error {
		v, err := b.client.Get(ctx, key).Bytes()
		out = v
		return err
	})
	if err != nil {
		return nil, err
	}
	// The client has already read the bulk reply; this only keeps oversized
	// data away from the decoder and the caller.
	if int64(len(out)) > b.maxValue {
		return nil, newError(ErrValueTooLarge, "max_value_bytes")
	}
	if out == nil {
		out = []byte{}
	}
	return out, nil
}

func (b *redisBackend) set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	return b.write(ctx, func(ctx context.Context) error {
		return b.client.Set(ctx, key, value, ttl).Err()
	})
}

func (b *redisBackend) setNX(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	var ok bool
	err := b.write(ctx, func(ctx context.Context) error {
		var err error
		ok, err = b.client.SetNX(ctx, key, value, ttl).Result()
		return err
	})
	return ok, err
}

func (b *redisBackend) del(ctx context.Context, keys []string) (int64, error) {
	var n int64
	err := b.write(ctx, func(ctx context.Context) error {
		var err error
		n, err = b.client.Del(ctx, keys...).Result()
		return err
	})
	return n, err
}

func (b *redisBackend) exists(ctx context.Context, key string) (bool, error) {
	var n int64
	err := b.read(ctx, func(ctx context.Context) error {
		var err error
		n, err = b.client.Exists(ctx, key).Result()
		return err
	})
	return n > 0, err
}

func (b *redisBackend) expire(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	var ok bool
	err := b.write(ctx, func(ctx context.Context) error {
		var err error
		ok, err = b.client.PExpire(ctx, key, ttl).Result()
		return err
	})
	return ok, err
}

func (b *redisBackend) ttl(ctx context.Context, key string) (Expiration, error) {
	var d time.Duration
	err := b.read(ctx, func(ctx context.Context) error {
		var err error
		d, err = b.client.PTTL(ctx, key).Result()
		return err
	})
	if err != nil {
		return Expiration{}, err
	}
	switch d {
	case -2: // go-redis keeps the PTTL sentinels unscaled
		return Expiration{}, nil
	case -1:
		return Expiration{Exists: true, Persistent: true}, nil
	default:
		return Expiration{Exists: true, Remaining: max(d, 0)}, nil
	}
}

func (b *redisBackend) incr(ctx context.Context, key string, delta int64) (int64, error) {
	var n int64
	err := b.write(ctx, func(ctx context.Context) error {
		var err error
		n, err = b.client.IncrBy(ctx, key, delta).Result()
		return err
	})
	return n, err
}

func (b *redisBackend) ping(ctx context.Context) error {
	return b.read(ctx, func(ctx context.Context) error {
		return b.client.Ping(ctx).Err()
	})
}

func (b *redisBackend) stop() {}

func (b *redisBackend) close() error {
	if err := b.client.Close(); err != nil && !errors.Is(err, redis.ErrClosed) {
		return newError(ErrUnavailable, "close")
	}
	return nil
}
