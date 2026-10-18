// Package cache is nForge's key/value cache: one non-generic *Cache backed
// by Redis or by a bounded in-process map, chosen by configuration.
//
// Objects (Get, Set, SetIfAbsent, GetOrLoad) are serialized with the
// framework codec registry in the configured format (json or cbor), with
// its payload limit, required fields and validators. Raw byte operations
// (GetBytes, SetBytes, SetBytesIfAbsent) and counters (Increment, decimal
// ASCII) bypass the codec. No envelope is added to stored values, so a key
// written with one codec cannot be read with another: changing cache.codec
// needs the old entries to expire, be invalidated, or a new cache.prefix.
//
// Every key is namespaced (see keyspace); with a tenant resolver the tenant
// of the operation's context is part of the namespace and is required.
//
// The package depends only on the codec package and the standard library
// (plus go-redis behind unexported adapters).
package cache

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"sync"
	"time"

	"github.com/ams-soft/nforge/codec"
)

// Cache is safe for concurrent use. Create it with Open and release it with
// Shutdown; after Shutdown every method returns ErrClosed.
type Cache struct {
	backend     backend
	keys        keyspace
	registry    *codec.Registry
	format      string
	maxValue    int64
	loadTimeout time.Duration
	maxFlights  int
	logger      *slog.Logger

	mu            sync.Mutex
	closed        bool
	active        int // admitted operations and running loaders
	drained       chan struct{}
	drainedClosed bool
	flights       map[string]*flight

	closing     context.Context // canceled when Shutdown starts: aborts loaders and waiters
	closeCancel context.CancelFunc
	closeOnce   sync.Once
	closeErr    error
}

// Open validates a deep copy of cfg (after ApplyDefaults), creates the
// selected driver and pings it with ctx. A disabled cfg returns ErrDisabled.
// On any failure the partially created resources are released. Open never
// panics or exits; deciding that a failure is fatal is the caller's job.
func Open(ctx context.Context, cfg Config, opts ...Option) (*Cache, error) {
	if !cfg.Enabled {
		return nil, ErrDisabled
	}
	cfg = cfg.clone()
	ApplyDefaults(&cfg)
	if err := ValidateConfig(cfg); err != nil {
		return nil, err
	}
	o, err := buildOptions(opts)
	if err != nil {
		return nil, err
	}
	ks, err := newKeyspace(cfg, o)
	if err != nil {
		return nil, err
	}
	if _, err := o.registry.Lookup(cfg.Codec); err != nil {
		return nil, &Error{Op: "open", Kind: ErrUnsupportedCodec, Reason: "not_enabled_in_registry"}
	}

	var b backend
	switch cfg.Driver {
	case DriverMemory:
		b = newMemory(cfg, o.now)
	default:
		rb, err := newRedis(cfg)
		if err != nil {
			return nil, err
		}
		b = rb
	}
	if err := b.ping(ctx); err != nil {
		_ = b.close()
		return nil, withOp(err, "open", b.name())
	}

	return assemble(cfg, o, ks, b), nil
}

// assemble builds the facade around an opened backend.
func assemble(cfg Config, o options, ks keyspace, b backend) *Cache {
	closing, cancel := context.WithCancel(context.Background())
	return &Cache{
		backend:     b,
		keys:        ks,
		registry:    o.registry,
		format:      cfg.Codec,
		maxValue:    int64(cfg.Limits.MaxValueBytes),
		loadTimeout: cfg.LoadTimeout,
		maxFlights:  cfg.Limits.MaxInflightLoads,
		logger:      o.logger,
		drained:     make(chan struct{}),
		flights:     make(map[string]*flight),
		closing:     closing,
		closeCancel: cancel,
	}
}

// Driver returns the active driver name ("redis" or "memory").
func (c *Cache) Driver() string { return c.backend.name() }

// begin admits an operation; every successful begin needs one end.
func (c *Cache) begin(ctx context.Context, op string) error {
	if err := ctx.Err(); err != nil {
		return c.fail(op, ctxError(err))
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return c.fail(op, newError(ErrClosed, ""))
	}
	c.active++
	return nil
}

func (c *Cache) end() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.active--
	c.maybeDrainedLocked()
}

func (c *Cache) maybeDrainedLocked() {
	if c.closed && c.active == 0 && !c.drainedClosed {
		c.drainedClosed = true
		close(c.drained)
	}
}

func (c *Cache) fail(op string, err error) error {
	if err == nil {
		return nil
	}
	return withOp(err, op, c.backend.name())
}

// Shutdown stops admitting operations (permanently: ErrClosed), stops the
// memory cleanup worker, cancels shared loaders and waiting GetOrLoad
// callers, waits for the operations in progress and then closes the driver
// (Redis pool, or clears the memory). If ctx ends first it returns the
// context error and leaves the cache closed; a later Shutdown keeps waiting
// for the remaining cooperative work. It is idempotent and safe to call
// concurrently.
func (c *Cache) Shutdown(ctx context.Context) error {
	c.mu.Lock()
	if !c.closed {
		c.closed = true
		c.closeCancel()
		c.maybeDrainedLocked()
	}
	c.mu.Unlock()
	c.backend.stop()

	select {
	case <-c.drained:
	case <-ctx.Done():
		return c.fail("shutdown", ctxError(ctx.Err()))
	}
	c.closeOnce.Do(func() { c.closeErr = c.backend.close() })
	return c.fail("shutdown", c.closeErr)
}

// Ping checks the backend (Redis PING; the memory driver only checks that
// the cache is open). It needs no tenant and is used by readiness probes.
func (c *Cache) Ping(ctx context.Context) error {
	const op = "ping"
	if err := c.begin(ctx, op); err != nil {
		return err
	}
	defer c.end()
	return c.fail(op, c.backend.ping(ctx))
}

// GetBytes returns an independent copy of a raw value; a present empty
// value is a non-nil empty slice. A missing key is ErrNotFound.
func (c *Cache) GetBytes(ctx context.Context, key string) ([]byte, error) {
	const op = "get_bytes"
	if err := c.begin(ctx, op); err != nil {
		return nil, err
	}
	defer c.end()
	full, err := c.keys.resolve(ctx, key)
	if err != nil {
		return nil, c.fail(op, err)
	}
	data, err := c.backend.get(ctx, full)
	return data, c.fail(op, err)
}

// SetBytes stores a copy of value unchanged (nil is an empty value, not a
// deletion), replacing any previous value and TTL. ttl 0 = no expiration.
func (c *Cache) SetBytes(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	const op = "set_bytes"
	if err := c.begin(ctx, op); err != nil {
		return err
	}
	defer c.end()
	full, ttl, err := c.prepareWrite(ctx, key, ttl, len(value))
	if err != nil {
		return c.fail(op, err)
	}
	return c.fail(op, c.backend.set(ctx, full, append([]byte{}, value...), ttl))
}

// SetBytesIfAbsent atomically stores value and its TTL only when no live
// entry exists. It returns false, nil when the key exists (value and TTL
// untouched). A lost Redis reply is ErrOutcomeUnknown, never false.
func (c *Cache) SetBytesIfAbsent(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	const op = "set_bytes_if_absent"
	if err := c.begin(ctx, op); err != nil {
		return false, err
	}
	defer c.end()
	full, ttl, err := c.prepareWrite(ctx, key, ttl, len(value))
	if err != nil {
		return false, c.fail(op, err)
	}
	ok, err := c.backend.setNX(ctx, full, append([]byte{}, value...), ttl)
	return ok, c.fail(op, err)
}

// prepareWrite validates the TTL, the key (tenant included) and the size.
func (c *Cache) prepareWrite(ctx context.Context, key string, ttl time.Duration, size int) (string, time.Duration, error) {
	ttl, err := normalizeTTL(ttl)
	if err != nil {
		return "", 0, err
	}
	full, err := c.keys.resolve(ctx, key)
	if err != nil {
		return "", 0, err
	}
	if int64(size) > c.maxValue {
		return "", 0, newError(ErrValueTooLarge, "max_value_bytes")
	}
	return full, ttl, nil
}

// Delete removes the given keys (exact keys only: no patterns) and returns
// how many distinct existing keys were removed. Every key is validated
// before anything is deleted; duplicates are removed once.
func (c *Cache) Delete(ctx context.Context, keys ...string) (int64, error) {
	const op = "delete"
	if err := c.begin(ctx, op); err != nil {
		return 0, err
	}
	defer c.end()
	if len(keys) == 0 {
		return 0, nil
	}
	full := make([]string, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		fk, err := c.keys.resolve(ctx, k)
		if err != nil {
			return 0, c.fail(op, err)
		}
		if _, dup := seen[fk]; !dup {
			seen[fk] = struct{}{}
			full = append(full, fk)
		}
	}
	n, err := c.backend.del(ctx, full)
	return n, c.fail(op, err)
}

// Exists reports whether a live entry exists (false, nil when missing or
// expired).
func (c *Cache) Exists(ctx context.Context, key string) (bool, error) {
	const op = "exists"
	if err := c.begin(ctx, op); err != nil {
		return false, err
	}
	defer c.end()
	full, err := c.keys.resolve(ctx, key)
	if err != nil {
		return false, c.fail(op, err)
	}
	ok, err := c.backend.exists(ctx, full)
	return ok, c.fail(op, err)
}

// Expire sets a new TTL (> 0) on an existing entry without touching its
// value; false, nil when the key is missing. To make an entry persistent,
// write it again with ttl 0.
func (c *Cache) Expire(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	const op = "expire"
	if err := c.begin(ctx, op); err != nil {
		return false, err
	}
	defer c.end()
	if ttl <= 0 {
		return false, c.fail(op, newError(ErrInvalidTTL, "not_positive"))
	}
	ttl, _ = normalizeTTL(ttl)
	full, err := c.keys.resolve(ctx, key)
	if err != nil {
		return false, c.fail(op, err)
	}
	ok, err := c.backend.expire(ctx, full, ttl)
	return ok, c.fail(op, err)
}

// TTL reports whether key exists and how long it has left.
func (c *Cache) TTL(ctx context.Context, key string) (Expiration, error) {
	const op = "ttl"
	if err := c.begin(ctx, op); err != nil {
		return Expiration{}, err
	}
	defer c.end()
	full, err := c.keys.resolve(ctx, key)
	if err != nil {
		return Expiration{}, c.fail(op, err)
	}
	exp, err := c.backend.ttl(ctx, full)
	return exp, c.fail(op, err)
}

// Increment atomically adds delta (any sign, or zero) to a decimal int64
// counter and returns the new value. A missing key starts at 0 and becomes
// a persistent counter; an existing TTL is preserved. Non-integer contents
// give ErrNotInteger and overflow ErrCounterOverflow, both without changing
// the entry. Counter keys must not be used for objects (and vice versa).
func (c *Cache) Increment(ctx context.Context, key string, delta int64) (int64, error) {
	const op = "increment"
	if err := c.begin(ctx, op); err != nil {
		return 0, err
	}
	defer c.end()
	full, err := c.keys.resolve(ctx, key)
	if err != nil {
		return 0, c.fail(op, err)
	}
	n, err := c.backend.incr(ctx, full, delta)
	return n, c.fail(op, err)
}

// checkTarget requires a non-nil pointer.
func checkTarget(target any) error {
	rv := reflect.ValueOf(target)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return newError(ErrInvalidTarget, "not_a_pointer")
	}
	return nil
}

// decodeInto decodes into a fresh value of the target's type and assigns it
// only on full success (codec limits, required fields and validation
// included), so the caller's target is never partially written.
func (c *Cache) decodeInto(data []byte, target any) error {
	rv := reflect.ValueOf(target)
	tmp := reflect.New(rv.Type().Elem())
	if err := c.registry.DecodeAs(c.format, data, tmp.Interface()); err != nil {
		return wrapError(ErrDecode, "", err)
	}
	rv.Elem().Set(tmp.Elem())
	return nil
}

func (c *Cache) encode(value any) ([]byte, error) {
	data, err := c.registry.EncodeAs(c.format, value)
	if err != nil {
		return nil, wrapError(ErrEncode, "", err)
	}
	return data, nil
}

// Get decodes the object stored at key into target (a non-nil pointer).
// On a miss (ErrNotFound) or any error the target is left unchanged. A
// decode failure is ErrDecode, never a miss.
func (c *Cache) Get(ctx context.Context, key string, target any) error {
	const op = "get"
	if err := c.begin(ctx, op); err != nil {
		return err
	}
	defer c.end()
	if err := checkTarget(target); err != nil {
		return c.fail(op, err)
	}
	full, err := c.keys.resolve(ctx, key)
	if err != nil {
		return c.fail(op, err)
	}
	data, err := c.backend.get(ctx, full)
	if err != nil {
		return c.fail(op, err)
	}
	return c.fail(op, c.decodeInto(data, target))
}

// Set encodes value with the configured codec and stores it, replacing any
// previous value and TTL. ttl 0 = no expiration.
func (c *Cache) Set(ctx context.Context, key string, value any, ttl time.Duration) error {
	const op = "set"
	if err := c.begin(ctx, op); err != nil {
		return err
	}
	defer c.end()
	full, w, err := c.prepareObject(ctx, key, value, ttl)
	if err != nil {
		return c.fail(op, err)
	}
	return c.fail(op, c.backend.set(ctx, full, w.data, w.ttl))
}

// SetIfAbsent is Set only when no live entry exists; see SetBytesIfAbsent.
func (c *Cache) SetIfAbsent(ctx context.Context, key string, value any, ttl time.Duration) (bool, error) {
	const op = "set_if_absent"
	if err := c.begin(ctx, op); err != nil {
		return false, err
	}
	defer c.end()
	full, w, err := c.prepareObject(ctx, key, value, ttl)
	if err != nil {
		return false, c.fail(op, err)
	}
	ok, err := c.backend.setNX(ctx, full, w.data, w.ttl)
	return ok, c.fail(op, err)
}

type encodedWrite struct {
	data []byte
	ttl  time.Duration
}

func (c *Cache) prepareObject(ctx context.Context, key string, value any, ttl time.Duration) (string, encodedWrite, error) {
	ttl, err := normalizeTTL(ttl)
	if err != nil {
		return "", encodedWrite{}, err
	}
	full, err := c.keys.resolve(ctx, key)
	if err != nil {
		return "", encodedWrite{}, err
	}
	data, err := c.encode(value)
	if err != nil {
		return "", encodedWrite{}, err
	}
	if int64(len(data)) > c.maxValue {
		return "", encodedWrite{}, newError(ErrValueTooLarge, "max_value_bytes")
	}
	return full, encodedWrite{data: data, ttl: ttl}, nil
}

// isNotFound reports a cache miss.
func isNotFound(err error) bool { return errors.Is(err, ErrNotFound) }
