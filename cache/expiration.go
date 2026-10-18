package cache

import (
	"context"
	"math"
	"time"
)

// Expiration is the result of TTL: a missing key, a persistent key or an
// expiring one. Redis's negative sentinels (-1, -2) never leak out.
type Expiration struct {
	Exists     bool
	Persistent bool
	// Remaining is the time left of an expiring key; never negative, and it
	// may round to zero while the key is still live.
	Remaining time.Duration
}

// maxTTL is the largest whole-millisecond time.Duration, the cap of normalizeTTL.
const maxTTL = time.Duration(math.MaxInt64/int64(time.Millisecond)) * time.Millisecond

// normalizeTTL rejects negative TTLs and rounds positive ones up to whole
// milliseconds (minimum 1ms, overflow-safe), so Redis (PX) and the memory
// driver expire at the same instant. Zero means "no expiration".
func normalizeTTL(ttl time.Duration) (time.Duration, error) {
	switch {
	case ttl < 0:
		return 0, newError(ErrInvalidTTL, "negative")
	case ttl == 0:
		return 0, nil
	case ttl >= maxTTL:
		return maxTTL, nil
	}
	if r := ttl % time.Millisecond; r != 0 {
		ttl += time.Millisecond - r
	}
	return ttl, nil
}

// backend is implemented by the drivers. Keys are already resolved full
// keys (never prefixed again) and TTLs are normalized. Values passed in are
// owned by the backend; returned values are owned by the caller. Errors are
// *Error values without Op/Driver (the facade fills them).
type backend interface {
	name() string
	get(ctx context.Context, key string) ([]byte, error)
	set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	setNX(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error)
	del(ctx context.Context, keys []string) (int64, error)
	exists(ctx context.Context, key string) (bool, error)
	expire(ctx context.Context, key string, ttl time.Duration) (bool, error)
	ttl(ctx context.Context, key string) (Expiration, error)
	incr(ctx context.Context, key string, delta int64) (int64, error)
	ping(ctx context.Context) error
	// stop halts background work (memory cleanup) at the start of shutdown.
	stop()
	// close releases the resources once no operation is running.
	close() error
}
