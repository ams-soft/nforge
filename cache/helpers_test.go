package cache

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ams-soft/nforge/codec"
)

var (
	errTestTenant = errors.New("no tenant")
	tenantA       = "11111111-1111-4111-8111-111111111111"
	tenantB       = "22222222-2222-4222-8222-222222222222"
)

type tenantKey struct{}

func withTenant(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, tenantKey{}, id)
}

func tenantFrom(ctx context.Context) (string, error) {
	id, _ := ctx.Value(tenantKey{}).(string)
	if id == "" {
		return "", errTestTenant
	}
	return id, nil
}

// fakeClock is the injected clock of the memory driver: no sleeps in
// expiration tests.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)} }

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func testRegistry(t testing.TB) *codec.Registry {
	t.Helper()
	r, err := codec.New(codec.Config{Formats: []string{"json", "cbor"}, Default: "json"})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func memoryConfig() Config {
	return Config{Enabled: true, Driver: DriverMemory}
}

// openMemory opens a memory cache with a fake clock; mutate adjusts the
// config before Open.
func openMemory(t testing.TB, mutate func(*Config), opts ...Option) (*Cache, *fakeClock) {
	t.Helper()
	cfg := memoryConfig()
	if mutate != nil {
		mutate(&cfg)
	}
	clock := newFakeClock()
	all := append([]Option{WithNamespace("test", "app"), WithCodecRegistry(testRegistry(t)), withClock(clock.now)}, opts...)
	c, err := Open(context.Background(), cfg, all...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Shutdown(context.Background()) })
	return c, clock
}

func mem(c *Cache) *memory {
	if f, ok := c.backend.(*faultBackend); ok {
		return f.backend.(*memory)
	}
	return c.backend.(*memory)
}

// faultBackend wraps a backend to inject errors and count calls.
type faultBackend struct {
	backend
	getErr  atomic.Pointer[error]
	setErr  atomic.Pointer[error]
	gets    atomic.Int32
	sets    atomic.Int32
	anyCall atomic.Int32
}

func wrapFaults(c *Cache) *faultBackend {
	f := &faultBackend{backend: c.backend}
	c.backend = f
	return f
}

func (f *faultBackend) failGets(err error) { f.getErr.Store(&err) }
func (f *faultBackend) failSets(err error) { f.setErr.Store(&err) }

func (f *faultBackend) get(ctx context.Context, key string) ([]byte, error) {
	f.gets.Add(1)
	f.anyCall.Add(1)
	if p := f.getErr.Load(); p != nil && *p != nil {
		return nil, *p
	}
	return f.backend.get(ctx, key)
}

func (f *faultBackend) set(ctx context.Context, key string, v []byte, ttl time.Duration) error {
	f.sets.Add(1)
	f.anyCall.Add(1)
	if p := f.setErr.Load(); p != nil && *p != nil {
		return *p
	}
	return f.backend.set(ctx, key, v, ttl)
}

func (f *faultBackend) setNX(ctx context.Context, key string, v []byte, ttl time.Duration) (bool, error) {
	f.anyCall.Add(1)
	return f.backend.setNX(ctx, key, v, ttl)
}

func (f *faultBackend) del(ctx context.Context, keys []string) (int64, error) {
	f.anyCall.Add(1)
	return f.backend.del(ctx, keys)
}

func (f *faultBackend) exists(ctx context.Context, key string) (bool, error) {
	f.anyCall.Add(1)
	return f.backend.exists(ctx, key)
}

func (f *faultBackend) incr(ctx context.Context, key string, d int64) (int64, error) {
	f.anyCall.Add(1)
	return f.backend.incr(ctx, key, d)
}

func mustIs(t testing.TB, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("err = %v, want %v", err, target)
	}
}

// User is the object used across tests; Validate rejects a negative age.
type User struct {
	ID   int64  `json:"id"`
	Name string `json:"name" codec:"required"`
	Age  int    `json:"age,omitempty"`
}

func (u *User) Validate() error {
	if u.Age < 0 {
		return errors.New("age must not be negative")
	}
	return nil
}

func newJSONOnly() (*codec.Registry, error) {
	return codec.New(codec.Config{Formats: []string{"json"}, Default: "json"})
}
