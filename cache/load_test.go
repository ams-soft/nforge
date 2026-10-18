package cache

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var errRepo = errors.New("repository: boom")

func TestGetOrLoadHitAvoidsLoader(t *testing.T) {
	c, _ := openMemory(t, nil)
	_ = c.Set(bg, "u", User{ID: 1, Name: "cached"}, 0)
	var u User
	err := c.GetOrLoad(bg, "u", &u, time.Minute, func(context.Context) (any, error) {
		t.Fatal("loader called on a hit")
		return nil, nil
	})
	if err != nil || u.Name != "cached" {
		t.Fatalf("%+v %v", u, err)
	}
}

func TestGetOrLoadMissLoadsAndStores(t *testing.T) {
	c, _ := openMemory(t, nil)
	var u User
	err := c.GetOrLoad(bg, "u", &u, time.Minute, func(context.Context) (any, error) {
		return &User{ID: 2, Name: "loaded"}, nil // a pointer result is fine too
	})
	if err != nil || u.Name != "loaded" {
		t.Fatalf("%+v %v", u, err)
	}
	if exp, _ := c.TTL(bg, "u"); exp.Remaining != time.Minute {
		t.Fatalf("stored with ttl %+v", exp)
	}
	var again User
	if err := c.Get(bg, "u", &again); err != nil || again != u {
		t.Fatalf("stored value: %+v %v", again, err)
	}
}

func TestGetOrLoadReadErrorSkipsLoader(t *testing.T) {
	c, _ := openMemory(t, nil)
	f := wrapFaults(c)
	f.failGets(newError(ErrUnavailable, "connection"))
	called := false
	target := User{Name: "keep"}
	err := c.GetOrLoad(bg, "u", &target, 0, func(context.Context) (any, error) {
		called = true
		return User{Name: "x"}, nil
	})
	mustIs(t, err, ErrUnavailable)
	if called || target.Name != "keep" {
		t.Fatal("loader must not run on a read outage")
	}

	// a corrupt value is not a miss either
	f.failGets(nil)
	_ = c.SetBytes(bg, "bad", []byte("{"), 0)
	err = c.GetOrLoad(bg, "bad", &target, 0, func(context.Context) (any, error) {
		called = true
		return User{Name: "x"}, nil
	})
	mustIs(t, err, ErrDecode)
	if called {
		t.Fatal("loader must not mask corrupt data")
	}
}

func TestGetOrLoadWriteFailureReturnsLoadedValue(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	c, _ := openMemory(t, nil, WithLogger(logger), WithTenantResolver(tenantFrom))
	f := wrapFaults(c)
	f.failSets(newError(ErrOutcomeUnknown, "timeout"))
	ctx := withTenant(bg, tenantA)

	var u User
	err := c.GetOrLoad(ctx, "secret-key", &u, 0, func(context.Context) (any, error) {
		return User{Name: "from-db"}, nil
	})
	if err != nil || u.Name != "from-db" {
		t.Fatalf("%+v %v", u, err)
	}
	out := logs.String()
	if !strings.Contains(out, "could not store") || !strings.Contains(out, "reason=timeout") {
		t.Fatalf("missing warning: %s", out)
	}
	if strings.Contains(out, "secret-key") || strings.Contains(out, tenantA) || strings.Contains(out, "from-db") {
		t.Fatalf("warning leaks data: %s", out)
	}
}

func TestGetOrLoadOversizedValueIsReturnedButNotStored(t *testing.T) {
	c, _ := openMemory(t, func(cfg *Config) { cfg.Limits.MaxValueBytes = 16 })
	var u User
	err := c.GetOrLoad(bg, "u", &u, 0, func(context.Context) (any, error) {
		return User{Name: strings.Repeat("n", 64)}, nil
	})
	if err != nil || len(u.Name) != 64 {
		t.Fatalf("%v", err)
	}
	if ok, _ := c.Exists(bg, "u"); ok {
		t.Fatal("oversized value must not be stored")
	}
}

func TestGetOrLoadLoaderErrorsAndPanics(t *testing.T) {
	c, _ := openMemory(t, nil)
	target := User{Name: "keep"}
	err := c.GetOrLoad(bg, "u", &target, 0, func(context.Context) (any, error) {
		return nil, errRepo
	})
	if !errors.Is(err, errRepo) || target.Name != "keep" {
		t.Fatalf("loader error must be returned unchanged: %v", err)
	}
	if ok, _ := c.Exists(bg, "u"); ok {
		t.Fatal("errors must not be cached")
	}

	err = c.GetOrLoad(bg, "u", &target, 0, func(context.Context) (any, error) {
		panic("secret panic data")
	})
	mustIs(t, err, ErrLoaderPanic)
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("panic data leaked: %v", err)
	}
	// the flight was released: the next call loads normally
	if err := c.GetOrLoad(bg, "u", &target, 0, func(context.Context) (any, error) {
		return User{Name: "ok"}, nil
	}); err != nil || target.Name != "ok" {
		t.Fatalf("after panic: %v", err)
	}

	// encode failures and invalid results are errors (no valid value to return)
	mustIs(t, c.GetOrLoad(bg, "enc", new(User), 0, func(context.Context) (any, error) {
		return make(chan int), nil
	}), ErrEncode)
	mustIs(t, c.GetOrLoad(bg, "inv", new(User), 0, func(context.Context) (any, error) {
		return User{Name: "x", Age: -1}, nil
	}), ErrDecode)
	mustIs(t, c.GetOrLoad(bg, "nil", new(User), 0, nil), ErrInvalidConfig)
	mustIs(t, c.GetOrLoad(bg, "neg", new(User), -1, func(context.Context) (any, error) { return nil, nil }), ErrInvalidTTL)
}

func TestGetOrLoadConcurrentCallersShareOneLoad(t *testing.T) {
	c, _ := openMemory(t, nil)
	var calls atomic.Int32
	release := make(chan struct{})
	const n = 20
	results := make([]*User, n)
	var wg sync.WaitGroup
	for i := range n {
		results[i] = &User{}
		wg.Go(func() {
			err := c.GetOrLoad(bg, "shared", results[i], time.Minute, func(context.Context) (any, error) {
				calls.Add(1)
				<-release
				return User{ID: 9, Name: "once"}, nil
			})
			if err != nil {
				t.Error(err)
			}
		})
	}
	waitFor(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		f := c.flights[mustResolve(t, c, bg, "shared")]
		return f != nil && f.waiters == n
	})
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("loader calls = %d", calls.Load())
	}
	for i, u := range results {
		if u.Name != "once" {
			t.Fatalf("result %d = %+v", i, u)
		}
		for j := range i {
			if results[j] == u {
				t.Fatal("targets must be independent")
			}
		}
	}
	// a different tenant/key never shares a flight: checked by key identity
	if len(c.flights) != 0 {
		t.Fatal("flight bookkeeping leaked")
	}
}

func TestGetOrLoadIncompatibleTargetFailsIndependently(t *testing.T) {
	c, _ := openMemory(t, nil)
	release := make(chan struct{})
	load := func(context.Context) (any, error) {
		<-release
		return User{ID: 1, Name: "n"}, nil
	}
	var good User
	var bad struct {
		Name int `json:"name"`
	}
	var wg sync.WaitGroup
	var errGood, errBad error
	wg.Go(func() { errGood = c.GetOrLoad(bg, "k", &good, 0, load) })
	waitFor(t, func() bool { c.mu.Lock(); defer c.mu.Unlock(); return len(c.flights) == 1 })
	wg.Go(func() { errBad = c.GetOrLoad(bg, "k", &bad, 0, load) })
	waitFor(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		for _, f := range c.flights {
			return f.waiters == 2
		}
		return false
	})
	close(release)
	wg.Wait()
	if errGood != nil || good.Name != "n" {
		t.Fatalf("good: %v %+v", errGood, good)
	}
	if !errors.Is(errBad, ErrDecode) || bad.Name != 0 {
		t.Fatalf("bad: %v %+v", errBad, bad)
	}
}

func TestGetOrLoadTenantsNeverShare(t *testing.T) {
	c, _ := openMemory(t, nil, WithTenantResolver(tenantFrom))
	var calls atomic.Int32
	load := func(ctx context.Context) (any, error) {
		calls.Add(1)
		id, _ := tenantFrom(ctx) // context values survive the detached loader context
		return User{Name: id}, nil
	}
	var a, b User
	_ = c.GetOrLoad(withTenant(bg, tenantA), "same", &a, 0, load)
	_ = c.GetOrLoad(withTenant(bg, tenantB), "same", &b, 0, load)
	if calls.Load() != 2 || a.Name != tenantA || b.Name != tenantB {
		t.Fatalf("calls=%d a=%q b=%q", calls.Load(), a.Name, b.Name)
	}
}

func TestGetOrLoadCanceledLeaderKeepsFlightForWaiters(t *testing.T) {
	c, _ := openMemory(t, nil)
	release := make(chan struct{})
	ctxCh := make(chan context.Context, 1)
	load := func(ctx context.Context) (any, error) {
		ctxCh <- ctx
		<-release
		return User{Name: "done"}, nil
	}
	leaderCtx, cancelLeader := context.WithCancel(bg)
	leaderErr := make(chan error, 1)
	go func() { leaderErr <- c.GetOrLoad(leaderCtx, "k", new(User), 0, load) }()
	waitFor(t, func() bool { c.mu.Lock(); defer c.mu.Unlock(); return len(c.flights) == 1 })

	var waiter User
	waiterErr := make(chan error, 1)
	go func() { waiterErr <- c.GetOrLoad(bg, "k", &waiter, 0, load) }()
	waitFor(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		for _, f := range c.flights {
			return f.waiters == 2
		}
		return false
	})

	cancelLeader()
	mustIs(t, <-leaderErr, context.Canceled)
	if loaderCtx := <-ctxCh; loaderCtx.Err() != nil {
		t.Fatal("the shared load must survive the first caller's cancellation")
	}
	close(release)
	if err := <-waiterErr; err != nil || waiter.Name != "done" {
		t.Fatalf("waiter: %v %+v", err, waiter)
	}
}

func TestGetOrLoadAllWaitersLeaveCancelsLoad(t *testing.T) {
	c, _ := openMemory(t, nil)
	loaderDone := make(chan error, 1)
	load := func(ctx context.Context) (any, error) {
		<-ctx.Done()
		loaderDone <- ctx.Err()
		return nil, ctx.Err()
	}
	ctx1, cancel1 := context.WithCancel(bg)
	ctx2, cancel2 := context.WithCancel(bg)
	errs := make(chan error, 2)
	go func() { errs <- c.GetOrLoad(ctx1, "k", new(User), 0, load) }()
	waitFor(t, func() bool { c.mu.Lock(); defer c.mu.Unlock(); return len(c.flights) == 1 })
	go func() { errs <- c.GetOrLoad(ctx2, "k", new(User), 0, load) }()
	waitFor(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		for _, f := range c.flights {
			return f.waiters == 2
		}
		return false
	})
	cancel1()
	cancel2()
	mustIs(t, <-errs, context.Canceled)
	mustIs(t, <-errs, context.Canceled)
	mustIs(t, <-loaderDone, context.Canceled)

	// a new caller after the abandoned flight gets a fresh load
	var u User
	if err := c.GetOrLoad(bg, "k", &u, 0, func(context.Context) (any, error) { return User{Name: "fresh"}, nil }); err != nil || u.Name != "fresh" {
		t.Fatalf("%v %+v", err, u)
	}
}

func TestGetOrLoadAbandonedFlightBlocksOverlap(t *testing.T) {
	c, _ := openMemory(t, nil)
	release := make(chan struct{})
	var running, maxRunning atomic.Int32
	load := func(context.Context) (any, error) {
		n := running.Add(1)
		if n > maxRunning.Load() {
			maxRunning.Store(n)
		}
		defer running.Add(-1)
		<-release // ignores cancellation
		return User{Name: "x"}, nil
	}
	ctx1, cancel1 := context.WithCancel(bg)
	done1 := make(chan struct{})
	go func() { _ = c.GetOrLoad(ctx1, "k", new(User), 0, load); close(done1) }()
	waitFor(t, func() bool { return running.Load() == 1 })
	cancel1()
	<-done1

	done2 := make(chan error, 1)
	go func() { done2 <- c.GetOrLoad(bg, "k", new(User), 0, load) }()
	time.Sleep(10 * time.Millisecond)
	if running.Load() != 1 {
		t.Fatal("a second loader started while the abandoned one still runs")
	}
	close(release)
	if err := <-done2; err != nil {
		t.Fatal(err)
	}
	if maxRunning.Load() != 1 {
		t.Fatalf("overlapping loads: %d", maxRunning.Load())
	}
}

func TestGetOrLoadCapacity(t *testing.T) {
	c, _ := openMemory(t, func(cfg *Config) { cfg.Limits.MaxInflightLoads = 1 })
	release := make(chan struct{})
	load := func(context.Context) (any, error) { <-release; return User{Name: "x"}, nil }
	errs := make(chan error, 2)
	go func() { errs <- c.GetOrLoad(bg, "a", new(User), 0, load) }()
	waitFor(t, func() bool { c.mu.Lock(); defer c.mu.Unlock(); return len(c.flights) == 1 })

	mustIs(t, c.GetOrLoad(bg, "b", new(User), 0, load), ErrLoadCapacity)
	go func() { errs <- c.GetOrLoad(bg, "a", new(User), 0, load) }() // joining is allowed
	waitFor(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		f := c.flights[mustResolve(t, c, bg, "a")]
		return f != nil && f.waiters == 2
	})
	close(release)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}

func TestGetOrLoadLoadTimeout(t *testing.T) {
	c, _ := openMemory(t, func(cfg *Config) { cfg.LoadTimeout = 10 * time.Millisecond })
	err := c.GetOrLoad(bg, "k", new(User), 0, func(ctx context.Context) (any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	mustIs(t, err, context.DeadlineExceeded)
}

func TestGetOrLoadShutdownCancelsWaitersAndLoaders(t *testing.T) {
	c, _ := openMemory(t, nil)
	loaderErr := make(chan error, 1)
	target := User{Name: "keep"}
	errs := make(chan error, 1)
	go func() {
		errs <- c.GetOrLoad(bg, "k", &target, 0, func(ctx context.Context) (any, error) {
			<-ctx.Done()
			loaderErr <- ctx.Err()
			return nil, ctx.Err()
		})
	}()
	waitFor(t, func() bool { c.mu.Lock(); defer c.mu.Unlock(); return len(c.flights) == 1 })
	if err := c.Shutdown(bg); err != nil {
		t.Fatal(err)
	}
	mustIs(t, <-errs, ErrClosed)
	mustIs(t, <-loaderErr, context.Canceled)
	if target.Name != "keep" {
		t.Fatal("target assigned on shutdown")
	}
}

func mustResolve(t testing.TB, c *Cache, ctx context.Context, key string) string {
	t.Helper()
	full, err := c.keys.resolve(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	return full
}

func waitFor(t testing.TB, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(time.Millisecond)
	}
}
