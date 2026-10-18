package cache

import (
	"bytes"
	"context"
	"errors"
	"time"
)

// flight is one shared load of a full key inside this Cache instance.
// Fields other than waiters are written by the leader goroutine before
// done is closed and only read after it.
type flight struct {
	done    chan struct{}
	data    []byte // encoded result, read-only once done
	err     error
	waiters int // guarded by Cache.mu; 0 = abandoned (loader canceled)
	cancel  context.CancelFunc
}

// GetOrLoad reads the object at key into target; on a miss it calls load,
// stores the encoded result with ttl and decodes it into target.
//
//   - A hit never calls load. Any read error other than ErrNotFound (Redis
//     down, corrupt data) is returned without calling load.
//   - Concurrent callers of the same full key (tenant included) in this
//     process share one load: the leader rechecks the cache, runs load once
//     with a context detached from the first caller (values kept,
//     cancellation not) and bounded by cache.load_timeout, and encodes the
//     result once. Each caller decodes its own copy into its own target.
//     The leader's loader and TTL govern the flight, so callers of one key
//     must use compatible loaders and TTLs. It is not a distributed lock:
//     other instances may load the same key concurrently.
//   - Load errors are returned unchanged (and not cached); application
//     errors must not be sent blindly to HTTP clients. A panic in load
//     becomes ErrLoaderPanic for every caller.
//   - Storing the result is best effort: a backend write failure, a full
//     memory cache or a value larger than max_value_bytes is logged as a
//     warning and the loaded value is still returned.
//   - A caller whose context ends returns its context error; the load is
//     canceled only when every caller has left (or on Shutdown).
//   - At most cache.limits.max_inflight_loads distinct flights run at once;
//     beyond that a new key fails with ErrLoadCapacity (joining an existing
//     flight is always allowed).
//
// target is only assigned on success.
func (c *Cache) GetOrLoad(ctx context.Context, key string, target any, ttl time.Duration,
	load func(context.Context) (any, error)) error {
	const op = "get_or_load"
	if err := c.begin(ctx, op); err != nil {
		return err
	}
	defer c.end()
	if err := checkTarget(target); err != nil {
		return c.fail(op, err)
	}
	if load == nil {
		return c.fail(op, newError(ErrInvalidConfig, "nil_loader"))
	}
	ttl, err := normalizeTTL(ttl)
	if err != nil {
		return c.fail(op, err)
	}
	full, err := c.keys.resolve(ctx, key)
	if err != nil {
		return c.fail(op, err)
	}

	data, err := c.backend.get(ctx, full)
	if err == nil {
		return c.fail(op, c.decodeInto(data, target))
	}
	if !isNotFound(err) {
		return c.fail(op, err)
	}

	f, err := c.join(ctx, full, ttl, load)
	if err != nil {
		return c.fail(op, err)
	}
	select {
	case <-f.done:
	case <-ctx.Done():
		c.leave(f)
		return c.fail(op, ctxError(ctx.Err()))
	case <-c.closing.Done():
		c.leave(f)
		return c.fail(op, newError(ErrClosed, ""))
	}
	if f.err != nil {
		return c.fail(op, f.err)
	}
	return c.fail(op, c.decodeInto(f.data, target))
}

// join attaches the caller to the running flight of full or starts one. A
// flight abandoned by all its callers keeps its slot until its loader
// returns (no overlapping loads of one key); a new caller waits for it to
// finish and then starts a fresh flight.
func (c *Cache) join(ctx context.Context, full string, ttl time.Duration, load func(context.Context) (any, error)) (*flight, error) {
	for {
		c.mu.Lock()
		f := c.flights[full]
		switch {
		case f != nil && f.waiters > 0:
			f.waiters++
			c.mu.Unlock()
			return f, nil
		case f != nil:
			c.mu.Unlock()
			select {
			case <-f.done:
				continue
			case <-ctx.Done():
				return nil, ctxError(ctx.Err())
			case <-c.closing.Done():
				return nil, newError(ErrClosed, "")
			}
		case c.closed:
			c.mu.Unlock()
			return nil, newError(ErrClosed, "")
		case len(c.flights) >= c.maxFlights:
			c.mu.Unlock()
			return nil, newError(ErrLoadCapacity, "")
		}

		lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.loadTimeout)
		stop := context.AfterFunc(c.closing, cancel)
		f = &flight{done: make(chan struct{}), waiters: 1, cancel: cancel}
		c.flights[full] = f
		c.active++ // the loader is lifecycle work: Shutdown waits for it
		c.mu.Unlock()

		go c.runFlight(lctx, full, f, ttl, load, func() { stop(); cancel() })
		return f, nil
	}
}

// leave detaches a caller; the last one cancels the shared load.
func (c *Cache) leave(f *flight) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f.waiters--
	if f.waiters == 0 {
		f.cancel()
	}
}

func (c *Cache) runFlight(ctx context.Context, full string, f *flight, ttl time.Duration,
	load func(context.Context) (any, error), release func()) {
	defer func() {
		release()
		c.mu.Lock()
		if c.flights[full] == f {
			delete(c.flights, full)
		}
		close(f.done)
		c.active--
		c.maybeDrainedLocked()
		c.mu.Unlock()
	}()
	f.data, f.err = c.loadOnce(ctx, full, ttl, load)
}

func (c *Cache) loadOnce(ctx context.Context, full string, ttl time.Duration,
	load func(context.Context) (any, error)) ([]byte, error) {
	// Recheck: another flight (or instance) may have filled the key while
	// this one was being set up.
	data, err := c.backend.get(ctx, full)
	if err == nil {
		return data, nil
	}
	if !isNotFound(err) {
		return nil, err
	}

	value, err := callLoader(ctx, load)
	if err != nil {
		if errors.Is(err, ErrLoaderPanic) {
			c.logger.ErrorContext(ctx, "[CacheModule] GetOrLoad loader panicked",
				"op", "get_or_load", "driver", c.backend.name())
		}
		return nil, err
	}
	data, err = c.encode(value)
	if err != nil {
		return nil, err
	}

	switch {
	case int64(len(data)) > c.maxValue:
		c.warnWrite(ctx, "max_value_bytes")
	default:
		if err := c.backend.set(ctx, full, bytes.Clone(data), ttl); err != nil {
			reason := "error"
			var ce *Error
			if errors.As(err, &ce) {
				reason = ce.Reason
				if reason == "" {
					reason = ce.Kind.Error()
				}
			}
			c.warnWrite(ctx, reason)
		}
	}
	return data, nil
}

// warnWrite logs a best-effort insertion failure without the key, tenant or
// value.
func (c *Cache) warnWrite(ctx context.Context, reason string) {
	c.logger.WarnContext(ctx, "[CacheModule] GetOrLoad could not store the loaded value",
		"op", "get_or_load", "driver", c.backend.name(), "reason", reason)
}

// callLoader runs load, converting a panic into ErrLoaderPanic (the panic
// value is never exposed to callers or logs).
func callLoader(ctx context.Context, load func(context.Context) (any, error)) (v any, err error) {
	defer func() {
		if r := recover(); r != nil {
			v, err = nil, newError(ErrLoaderPanic, "")
		}
	}()
	return load(ctx)
}
