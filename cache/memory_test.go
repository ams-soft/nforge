package cache

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"
)

var bg = context.Background()

func TestMemoryRawSemantics(t *testing.T) {
	c, _ := openMemory(t, nil)

	if _, err := c.GetBytes(bg, "missing"); err == nil {
		t.Fatal("missing key must fail")
	} else {
		mustIs(t, err, ErrNotFound)
	}

	// nil input is a present empty value, not a deletion
	if err := c.SetBytes(bg, "empty", nil, 0); err != nil {
		t.Fatal(err)
	}
	got, err := c.GetBytes(bg, "empty")
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("empty value: %v %#v", err, got)
	}

	// the stored value is a copy of the input; the result a copy of the stored
	in := []byte("hello")
	_ = c.SetBytes(bg, "k", in, 0)
	in[0] = 'X'
	got, _ = c.GetBytes(bg, "k")
	if string(got) != "hello" {
		t.Fatalf("stored value aliased the input: %q", got)
	}
	got[0] = 'Y'
	again, _ := c.GetBytes(bg, "k")
	if string(again) != "hello" {
		t.Fatalf("returned value aliased the store: %q", again)
	}
}

func TestMemoryTTLSemantics(t *testing.T) {
	c, clock := openMemory(t, nil)

	exp, err := c.TTL(bg, "missing")
	if err != nil || exp != (Expiration{}) {
		t.Fatalf("missing: %+v %v", exp, err)
	}
	_ = c.SetBytes(bg, "p", []byte("v"), 0)
	if exp, _ := c.TTL(bg, "p"); !exp.Exists || !exp.Persistent {
		t.Fatalf("persistent: %+v", exp)
	}

	// positive TTLs round up to whole milliseconds
	_ = c.SetBytes(bg, "e", []byte("v"), 1500*time.Microsecond)
	if exp, _ := c.TTL(bg, "e"); !exp.Exists || exp.Persistent || exp.Remaining != 2*time.Millisecond {
		t.Fatalf("expiring: %+v", exp)
	}
	clock.advance(2*time.Millisecond - time.Nanosecond)
	if ok, _ := c.Exists(bg, "e"); !ok {
		t.Fatal("must be live just before the deadline")
	}
	clock.advance(time.Nanosecond) // now == deadline: expired
	if ok, _ := c.Exists(bg, "e"); ok {
		t.Fatal("must be expired at the deadline")
	}
	if _, err := c.GetBytes(bg, "e"); err == nil {
		t.Fatal("expired entry returned")
	}

	// Set replaces the TTL; ttl 0 makes it persistent again
	_ = c.SetBytes(bg, "r", []byte("v"), time.Second)
	_ = c.SetBytes(bg, "r", []byte("v2"), 0)
	clock.advance(time.Hour)
	if got, err := c.GetBytes(bg, "r"); err != nil || string(got) != "v2" {
		t.Fatalf("overwrite must clear the TTL: %q %v", got, err)
	}

	// Expire
	if ok, err := c.Expire(bg, "missing", time.Second); ok || err != nil {
		t.Fatalf("expire missing: %v %v", ok, err)
	}
	if ok, err := c.Expire(bg, "r", time.Second); !ok || err != nil {
		t.Fatalf("expire: %v %v", ok, err)
	}
	if got, _ := c.GetBytes(bg, "r"); string(got) != "v2" {
		t.Fatal("expire must keep the value")
	}
	if _, err := c.Expire(bg, "r", 0); err == nil {
		t.Fatal("expire with 0 must fail")
	} else {
		mustIs(t, err, ErrInvalidTTL)
	}
	clock.advance(time.Second)
	if ok, _ := c.Exists(bg, "r"); ok {
		t.Fatal("expired by Expire")
	}

	if err := c.SetBytes(bg, "neg", []byte("v"), -time.Second); err == nil {
		t.Fatal("negative ttl must fail")
	} else {
		mustIs(t, err, ErrInvalidTTL)
	}
}

func TestNormalizeTTL(t *testing.T) {
	cases := map[time.Duration]time.Duration{
		0:                       0,
		1:                       time.Millisecond,
		time.Millisecond:        time.Millisecond,
		time.Millisecond + 1:    2 * time.Millisecond,
		time.Second:             time.Second,
		math.MaxInt64:           maxTTL,
		maxTTL - 1:              maxTTL,
		maxTTL:                  maxTTL,
		1500 * time.Microsecond: 2 * time.Millisecond,
	}
	for in, want := range cases {
		got, err := normalizeTTL(in)
		if err != nil || got != want {
			t.Errorf("normalizeTTL(%d) = %d, %v; want %d", in, got, err, want)
		}
	}
	if _, err := normalizeTTL(-1); err == nil {
		t.Fatal("negative must fail")
	}
}

func TestMemorySetIfAbsent(t *testing.T) {
	c, clock := openMemory(t, nil)
	ok, err := c.SetBytesIfAbsent(bg, "k", []byte("first"), time.Second)
	if !ok || err != nil {
		t.Fatalf("first: %v %v", ok, err)
	}
	ok, err = c.SetBytesIfAbsent(bg, "k", []byte("second"), 0)
	if ok || err != nil {
		t.Fatalf("second: %v %v", ok, err)
	}
	if got, _ := c.GetBytes(bg, "k"); string(got) != "first" {
		t.Fatalf("value changed: %q", got)
	}
	if exp, _ := c.TTL(bg, "k"); exp.Persistent || exp.Remaining != time.Second {
		t.Fatalf("ttl changed: %+v", exp)
	}
	clock.advance(time.Second) // an expired entry counts as absent
	if ok, _ := c.SetBytesIfAbsent(bg, "k", []byte("third"), 0); !ok {
		t.Fatal("expired entry must count as absent")
	}
}

func TestMemorySetIfAbsentIsAtomic(t *testing.T) {
	c, _ := openMemory(t, nil)
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for i := range 64 {
		wg.Go(func() {
			ok, err := c.SetBytesIfAbsent(bg, "race", []byte(strconv.Itoa(i)), 0)
			if err != nil {
				t.Error(err)
			}
			if ok {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("wins = %d, want 1", wins)
	}
}

func TestMemoryDelete(t *testing.T) {
	c, clock := openMemory(t, nil)
	_ = c.SetBytes(bg, "a", []byte("1"), 0)
	_ = c.SetBytes(bg, "b", []byte("2"), 0)
	_ = c.SetBytes(bg, "x", []byte("3"), time.Millisecond)
	clock.advance(time.Millisecond)

	n, err := c.Delete(bg, "a", "a", "b", "missing", "x")
	if err != nil || n != 2 {
		t.Fatalf("delete = %d, %v; want 2 (distinct, existing, live)", n, err)
	}
	if n, err := c.Delete(bg); n != 0 || err != nil {
		t.Fatalf("no keys: %d %v", n, err)
	}
	// every key is validated before any mutation
	_ = c.SetBytes(bg, "keep", []byte("v"), 0)
	if _, err := c.Delete(bg, "keep", ""); err == nil {
		t.Fatal("empty key must fail")
	}
	if ok, _ := c.Exists(bg, "keep"); !ok {
		t.Fatal("a failed Delete must not delete anything")
	}
}

func TestMemoryIncrement(t *testing.T) {
	c, clock := openMemory(t, nil)

	if n, err := c.Increment(bg, "n", 5); n != 5 || err != nil {
		t.Fatalf("absent: %d %v", n, err)
	}
	if exp, _ := c.TTL(bg, "n"); !exp.Persistent {
		t.Fatalf("a new counter is persistent: %+v", exp)
	}
	if n, _ := c.Increment(bg, "n", -7); n != -2 {
		t.Fatalf("negative delta: %d", n)
	}
	if n, _ := c.Increment(bg, "n", 0); n != -2 {
		t.Fatalf("zero delta: %d", n)
	}
	if got, _ := c.GetBytes(bg, "n"); string(got) != "-2" {
		t.Fatalf("stored as decimal: %q", got)
	}

	// the TTL is preserved
	_ = c.SetBytes(bg, "t", []byte("10"), 10*time.Second)
	clock.advance(4 * time.Second)
	if n, _ := c.Increment(bg, "t", 1); n != 11 {
		t.Fatalf("t = %d", n)
	}
	if exp, _ := c.TTL(bg, "t"); exp.Remaining != 6*time.Second {
		t.Fatalf("ttl after increment: %+v", exp)
	}

	for _, bad := range []string{"", "abc", "1.5", "+1", " 1", "1 ", "01", "-0", "-", "9223372036854775808", `"1"`} {
		_ = c.SetBytes(bg, "bad", []byte(bad), 0)
		if _, err := c.Increment(bg, "bad", 1); err == nil {
			t.Fatalf("%q must not be an integer", bad)
		} else {
			mustIs(t, err, ErrNotInteger)
		}
		if got, _ := c.GetBytes(bg, "bad"); string(got) != bad {
			t.Fatalf("%q changed to %q", bad, got)
		}
	}
	for _, good := range []string{"0", "-1", "9223372036854775807", "-9223372036854775808"} {
		if _, ok := parseCounter([]byte(good)); !ok {
			t.Fatalf("%q must parse", good)
		}
	}

	_ = c.SetBytes(bg, "max", []byte(strconv.FormatInt(math.MaxInt64, 10)), 0)
	if _, err := c.Increment(bg, "max", 1); err == nil {
		t.Fatal("overflow must fail")
	} else {
		mustIs(t, err, ErrCounterOverflow)
	}
	_ = c.SetBytes(bg, "min", []byte(strconv.FormatInt(math.MinInt64, 10)), 0)
	if _, err := c.Increment(bg, "min", -1); err == nil {
		t.Fatal("underflow must fail")
	}
	if got, _ := c.GetBytes(bg, "max"); string(got) != strconv.FormatInt(math.MaxInt64, 10) {
		t.Fatal("overflow changed the entry")
	}
}

func TestMemoryConcurrentIncrements(t *testing.T) {
	c, _ := openMemory(t, nil)
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			for range 100 {
				if _, err := c.Increment(bg, "hits", 1); err != nil {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()
	if got, _ := c.GetBytes(bg, "hits"); string(got) != "5000" {
		t.Fatalf("hits = %s", got)
	}
}

func TestMemoryEntryLimitAndLRU(t *testing.T) {
	c, _ := openMemory(t, func(cfg *Config) { cfg.Memory.MaxEntries = 3 })
	for _, k := range []string{"a", "b", "c"} {
		_ = c.SetBytes(bg, k, []byte(k), 0)
	}
	_, _ = c.GetBytes(bg, "a")              // a becomes most recent: order (old→new) b, c, a
	_, _ = c.Exists(bg, "b")                // metadata probes do not change recency
	_, _ = c.TTL(bg, "b")                   //
	_, _ = c.Expire(bg, "b", time.Hour)     //
	_ = c.SetBytes(bg, "d", []byte("d"), 0) // evicts b, the least recently used
	if ok, _ := c.Exists(bg, "b"); ok {
		t.Fatal("b should have been evicted")
	}
	for _, k := range []string{"a", "c", "d"} {
		if ok, _ := c.Exists(bg, k); !ok {
			t.Fatalf("%s evicted", k)
		}
	}
	m := mem(c)
	if len(m.items) != 3 {
		t.Fatalf("entries = %d", len(m.items))
	}
}

func TestMemoryByteLimitAndAccounting(t *testing.T) {
	c, _ := openMemory(t, func(cfg *Config) {
		cfg.Limits.MaxKeyBytes = 64
		cfg.Limits.MaxValueBytes = 100
		cfg.Memory.MaxBytes = 300
	})
	m := mem(c)
	keyLen := int64(len("nforge:test:app:") + 1)

	_ = c.SetBytes(bg, "a", bytes.Repeat([]byte("x"), 100), 0)
	_ = c.SetBytes(bg, "b", bytes.Repeat([]byte("x"), 100), 0)
	if m.bytes != 2*(keyLen+100) {
		t.Fatalf("bytes = %d", m.bytes)
	}
	// replacement subtracts the old entry first: no eviction needed
	_ = c.SetBytes(bg, "a", bytes.Repeat([]byte("y"), 50), 0)
	if m.bytes != (keyLen+100)+(keyLen+50) || len(m.items) != 2 {
		t.Fatalf("after replacement: bytes=%d items=%d", m.bytes, len(m.items))
	}
	// c does not fit with a and b: b is the LRU and goes
	_ = c.SetBytes(bg, "c", bytes.Repeat([]byte("z"), 100), 0)
	if ok, _ := c.Exists(bg, "b"); ok {
		t.Fatal("b should have been evicted")
	}
	if m.bytes > 300 {
		t.Fatalf("over budget: %d", m.bytes)
	}

	// a value over max_value_bytes is rejected without touching the old entry
	before := m.bytes
	if err := c.SetBytes(bg, "a", bytes.Repeat([]byte("w"), 101), 0); err == nil {
		t.Fatal("oversized value accepted")
	} else {
		mustIs(t, err, ErrValueTooLarge)
	}
	if got, _ := c.GetBytes(bg, "a"); string(got) != string(bytes.Repeat([]byte("y"), 50)) || m.bytes != before {
		t.Fatal("rejected write changed the existing state")
	}
}

func TestMemoryCapacityRejectionKeepsState(t *testing.T) {
	// max_value_bytes is allowed to exceed what a long key leaves in max_bytes
	// only through the constructor below; the validated config never allows it,
	// so test the driver directly.
	clock := newFakeClock()
	cfg := memoryConfig()
	ApplyDefaults(&cfg)
	cfg.Memory.MaxBytes = 20
	cfg.Limits.MaxValueBytes = 1000
	m := newMemory(cfg, clock.now)
	defer func() { _ = m.close() }()

	if err := m.set(bg, "k", []byte("12345"), 0); err != nil {
		t.Fatal(err)
	}
	err := m.set(bg, "big", bytes.Repeat([]byte("x"), 30), 0)
	mustIs(t, err, ErrValueTooLarge)
	if got, err := m.get(bg, "k"); err != nil || string(got) != "12345" {
		t.Fatal("capacity rejection evicted other entries")
	}
	if err := m.set(bg, "k", bytes.Repeat([]byte("x"), 30), 0); err == nil {
		t.Fatal("must fail")
	}
	if got, _ := m.get(bg, "k"); string(got) != "12345" {
		t.Fatal("capacity rejection deleted the previous entry")
	}
	// a counter that would no longer fit fails without changing the entry
	_ = m.set(bg, "counter-key-xxx", []byte("99999"), 0) // 15 + 5 = 20 bytes: full
	if _, err := m.incr(bg, "counter-key-xxx", 1); err == nil {
		t.Fatal("counter growth over capacity must fail")
	}
	if got, _ := m.get(bg, "counter-key-xxx"); string(got) != "99999" {
		t.Fatalf("counter changed: %s", got)
	}
}

func TestMemoryExpiredEntriesReclaimedBeforeLRU(t *testing.T) {
	c, clock := openMemory(t, func(cfg *Config) { cfg.Memory.MaxEntries = 2 })
	_ = c.SetBytes(bg, "old", []byte("v"), 0)
	_ = c.SetBytes(bg, "short", []byte("v"), time.Millisecond)
	clock.advance(time.Millisecond)
	_ = c.SetBytes(bg, "new", []byte("v"), 0)
	if ok, _ := c.Exists(bg, "old"); !ok {
		t.Fatal("the expired entry must be reclaimed before evicting live ones")
	}
}

func TestMemoryCleanup(t *testing.T) {
	c, clock := openMemory(t, nil)
	m := mem(c)
	for i := range cleanupBatch + 10 {
		_ = c.SetBytes(bg, fmt.Sprint("e", i), []byte("v"), time.Second)
	}
	_ = c.SetBytes(bg, "persistent", []byte("v"), 0)
	_ = c.SetBytes(bg, "later", []byte("v"), time.Hour)
	clock.advance(time.Second)

	if n := m.cleanupOnce(); n != cleanupBatch {
		t.Fatalf("first batch = %d", n)
	}
	if n := m.cleanupOnce(); n != 10 {
		t.Fatalf("second batch = %d", n)
	}
	if n := m.cleanupOnce(); n != 0 {
		t.Fatalf("third batch = %d", n)
	}
	if len(m.items) != 2 || len(m.exp) != 1 || m.bytes != int64(len("nforge:test:app:persistent")+len("nforge:test:app:later")+2) {
		t.Fatalf("items=%d heap=%d bytes=%d", len(m.items), len(m.exp), m.bytes)
	}
}

func TestMemoryWorkerStopsOnShutdown(t *testing.T) {
	before := runtime.NumGoroutine()
	for range 20 {
		c, err := Open(bg, Config{Enabled: true, Driver: DriverMemory, Memory: MemoryConfig{CleanupInterval: time.Millisecond}},
			WithNamespace("e", "a"))
		if err != nil {
			t.Fatal(err)
		}
		_ = c.SetBytes(bg, "k", []byte("v"), time.Millisecond)
		if err := c.Shutdown(bg); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		runtime.Gosched()
	}
	if n := runtime.NumGoroutine(); n > before {
		t.Fatalf("goroutines: before %d, after %d", before, n)
	}
}

func TestMemoryCleanupWorkerRuns(t *testing.T) {
	clock := newFakeClock()
	cfg := memoryConfig()
	ApplyDefaults(&cfg)
	cfg.Memory.CleanupInterval = time.Millisecond
	m := newMemory(cfg, clock.now)
	defer func() { _ = m.close() }()
	_ = m.set(bg, "k", []byte("v"), time.Second)
	clock.advance(time.Second)
	deadline := time.Now().Add(5 * time.Second)
	for {
		m.mu.Lock()
		n := len(m.items)
		m.mu.Unlock()
		if n == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("worker did not remove the expired entry")
		}
		runtime.Gosched()
	}
}
