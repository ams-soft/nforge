//go:build integration

package cache

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"math"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// Run with a disposable Redis (never a shared production server):
//
//	NFORGE_TEST_REDIS_URL=redis://localhost:6379/15 go test -tags integration ./cache/
//
// Every test uses a unique prefix and deletes only the keys it wrote; the
// database is never flushed.

func integrationCache(t *testing.T, mutate func(*Config)) (*Cache, *redis.Client, string) {
	t.Helper()
	url := os.Getenv("NFORGE_TEST_REDIS_URL")
	if url == "" {
		t.Skip("NFORGE_TEST_REDIS_URL not set")
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	prefix := "nforge-test:" + hex.EncodeToString(b)
	cfg := Config{Enabled: true, Driver: DriverRedis, Prefix: prefix, Redis: RedisConfig{URL: url}}
	if mutate != nil {
		mutate(&cfg)
	}
	c, err := Open(context.Background(), cfg, WithCodecRegistry(testRegistry(t)))
	if err != nil {
		t.Fatal(err)
	}
	opts, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	raw := redis.NewClient(opts)
	t.Cleanup(func() {
		ctx := context.Background()
		// delete only this test's keys (unique prefix), never FLUSHDB
		iter := raw.Scan(ctx, 0, prefix+":*", 100).Iterator()
		for iter.Next(ctx) {
			raw.Del(ctx, iter.Val())
		}
		_ = raw.Close()
		_ = c.Shutdown(ctx)
	})
	return c, raw, prefix + ":"
}

func TestIntegrationRawAndTTL(t *testing.T) {
	c, raw, prefix := integrationCache(t, nil)
	ctx := context.Background()

	_, err := c.GetBytes(ctx, "missing")
	mustIs(t, err, ErrNotFound)
	if err := c.SetBytes(ctx, "empty", nil, 0); err != nil {
		t.Fatal(err)
	}
	got, err := c.GetBytes(ctx, "empty")
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("empty: %#v %v", got, err)
	}
	// stored under the resolved key, unchanged
	_ = c.SetBytes(ctx, "k", []byte("raw\x00bytes"), 0)
	if v, _ := raw.Get(ctx, prefix+"k").Result(); v != "raw\x00bytes" {
		t.Fatalf("redis value = %q", v)
	}

	if exp, _ := c.TTL(ctx, "missing"); exp != (Expiration{}) {
		t.Fatalf("missing: %+v", exp)
	}
	if exp, _ := c.TTL(ctx, "k"); !exp.Exists || !exp.Persistent {
		t.Fatalf("persistent: %+v", exp)
	}
	_ = c.SetBytes(ctx, "e", []byte("v"), 1500*time.Microsecond) // rounded up to 2ms (PX 2)
	if exp, _ := c.TTL(ctx, "e"); exp.Exists && (exp.Persistent || exp.Remaining > 2*time.Millisecond || exp.Remaining < 0) {
		t.Fatalf("expiring: %+v", exp)
	}
	time.Sleep(10 * time.Millisecond)
	if ok, _ := c.Exists(ctx, "e"); ok {
		t.Fatal("expired key still exists")
	}

	_ = c.SetBytes(ctx, "r", []byte("v"), time.Minute)
	_ = c.SetBytes(ctx, "r", []byte("v2"), 0)
	if exp, _ := c.TTL(ctx, "r"); !exp.Persistent {
		t.Fatalf("overwrite must clear the TTL: %+v", exp)
	}
	if ok, _ := c.Expire(ctx, "r", time.Minute); !ok {
		t.Fatal("expire")
	}
	if exp, _ := c.TTL(ctx, "r"); exp.Persistent || exp.Remaining <= 59*time.Second || exp.Remaining > time.Minute {
		t.Fatalf("after expire: %+v", exp)
	}
	if ok, err := c.Expire(ctx, "missing", time.Minute); ok || err != nil {
		t.Fatalf("expire missing: %v %v", ok, err)
	}

	if ok, _ := c.SetBytesIfAbsent(ctx, "nx", []byte("1"), time.Minute); !ok {
		t.Fatal("nx first")
	}
	if ok, err := c.SetBytesIfAbsent(ctx, "nx", []byte("2"), 0); ok || err != nil {
		t.Fatalf("nx second: %v %v", ok, err)
	}
	if v, _ := c.GetBytes(ctx, "nx"); string(v) != "1" {
		t.Fatal("nx changed the value")
	}
	if exp, _ := c.TTL(ctx, "nx"); exp.Persistent {
		t.Fatal("nx changed the ttl")
	}

	n, err := c.Delete(ctx, "k", "k", "nx", "missing")
	if n != 2 || err != nil {
		t.Fatalf("delete = %d %v", n, err)
	}
}

func TestIntegrationNXAtomicity(t *testing.T) {
	c, _, _ := integrationCache(t, nil)
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for i := range 32 {
		wg.Go(func() {
			ok, err := c.SetBytesIfAbsent(context.Background(), "race", []byte(strconv.Itoa(i)), time.Minute)
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
		t.Fatalf("wins = %d", wins)
	}
}

func TestIntegrationCounters(t *testing.T) {
	c, _, _ := integrationCache(t, nil)
	ctx := context.Background()
	if n, _ := c.Increment(ctx, "n", 5); n != 5 {
		t.Fatalf("n = %d", n)
	}
	if n, _ := c.Increment(ctx, "n", -7); n != -2 {
		t.Fatalf("n = %d", n)
	}
	if exp, _ := c.TTL(ctx, "n"); !exp.Persistent {
		t.Fatal("new counter must be persistent")
	}
	_ = c.SetBytes(ctx, "t", []byte("10"), time.Minute)
	_, _ = c.Increment(ctx, "t", 1)
	if exp, _ := c.TTL(ctx, "t"); exp.Persistent {
		t.Fatal("increment must preserve the ttl")
	}
	for _, bad := range []string{"abc", "1.5", " 1"} {
		_ = c.SetBytes(ctx, "bad", []byte(bad), 0)
		_, err := c.Increment(ctx, "bad", 1)
		mustIs(t, err, ErrNotInteger)
	}
	_ = c.SetBytes(ctx, "max", []byte(strconv.FormatInt(math.MaxInt64, 10)), 0)
	_, err := c.Increment(ctx, "max", 1)
	mustIs(t, err, ErrCounterOverflow)

	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			for range 50 {
				if _, err := c.Increment(ctx, "hits", 1); err != nil {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()
	if v, _ := c.GetBytes(ctx, "hits"); string(v) != "1000" {
		t.Fatalf("hits = %s", v)
	}
}

func TestIntegrationObjectsAndLoad(t *testing.T) {
	for _, format := range []string{CodecJSON, CodecCBOR} {
		t.Run(format, func(t *testing.T) {
			c, _, _ := integrationCache(t, func(cfg *Config) { cfg.Codec = format })
			ctx := context.Background()
			in := User{ID: 1, Name: "Ana"}
			if err := c.Set(ctx, "u", in, time.Minute); err != nil {
				t.Fatal(err)
			}
			var out User
			if err := c.Get(ctx, "u", &out); err != nil || out != in {
				t.Fatalf("%+v %v", out, err)
			}
			calls := 0
			load := func(context.Context) (any, error) { calls++; return User{ID: 2, Name: "Bia"}, nil }
			var a, b User
			if err := c.GetOrLoad(ctx, "l", &a, time.Minute, load); err != nil || a.Name != "Bia" {
				t.Fatal(err)
			}
			if err := c.GetOrLoad(ctx, "l", &b, time.Minute, load); err != nil || b.Name != "Bia" || calls != 1 {
				t.Fatalf("second load: calls=%d %v", calls, err)
			}
		})
	}
}

func TestIntegrationTenantNamespace(t *testing.T) {
	c, raw, prefix := integrationCache(t, nil)
	c.keys.tenant = tenantFrom
	ctx := withTenant(context.Background(), tenantA)
	_ = c.SetBytes(ctx, "k", []byte("v"), 0)
	if v, _ := raw.Get(context.Background(), prefix+tenantA+":k").Result(); v != "v" {
		t.Fatal("tenant key not found in redis")
	}
	_, err := c.GetBytes(context.Background(), "k")
	mustIs(t, err, ErrTenantRequired)
}

func TestIntegrationPingAndBadAddress(t *testing.T) {
	c, _, _ := integrationCache(t, nil)
	if err := c.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, err := Open(context.Background(), Config{Enabled: true, Prefix: "x", Redis: RedisConfig{
		Host: "127.0.0.1", Port: 1, Timeouts: RedisTimeouts{Dial: 200 * time.Millisecond},
	}})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("closed port: %v", err)
	}
}

// replyDroppingProxy forwards to Redis; while drop is set it closes the
// client connection instead of relaying a reply, so the command executes
// on the server but the client never learns the outcome.
type replyDroppingProxy struct {
	ln   net.Listener
	drop atomic.Bool
}

func newReplyDroppingProxy(t *testing.T, target string) *replyDroppingProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &replyDroppingProxy{ln: ln}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			client, err := ln.Accept()
			if err != nil {
				return
			}
			server, err := net.Dial("tcp", target)
			if err != nil {
				_ = client.Close()
				continue
			}
			go func() { _, _ = io.Copy(server, client); _ = server.Close() }()
			go func() {
				buf := make([]byte, 32*1024)
				for {
					n, err := server.Read(buf)
					if n > 0 && p.drop.Load() {
						_ = client.Close()
						_ = server.Close()
						return
					}
					if n > 0 {
						if _, werr := client.Write(buf[:n]); werr != nil {
							return
						}
					}
					if err != nil {
						_ = client.Close()
						return
					}
				}
			}()
		}
	}()
	return p
}

func TestIntegrationIncrementNotDuplicatedAfterLostReply(t *testing.T) {
	_, raw, prefix := integrationCache(t, nil)
	opts, _ := redis.ParseURL(os.Getenv("NFORGE_TEST_REDIS_URL"))
	proxy := newReplyDroppingProxy(t, opts.Addr)

	host, port, _ := net.SplitHostPort(proxy.ln.Addr().String())
	p, _ := strconv.Atoi(port)
	c, err := Open(context.Background(), Config{Enabled: true, Prefix: strings.TrimSuffix(prefix, ":"),
		Redis: RedisConfig{Host: host, Port: p, Database: opts.DB}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Shutdown(context.Background())

	proxy.drop.Store(true)
	_, err = c.Increment(context.Background(), "counter", 1)
	mustIs(t, err, ErrOutcomeUnknown)
	proxy.drop.Store(false)

	// executed exactly once on the server, never replayed by the client
	if v, _ := raw.Get(context.Background(), prefix+"counter").Result(); v != "1" {
		t.Fatalf("counter = %q, want 1", v)
	}
	if n, err := c.Increment(context.Background(), "counter", 1); n != 2 || err != nil {
		t.Fatalf("after the lost reply the same cache must work: %d %v", n, err)
	}
}

// BenchmarkIntegrationRedis measures facade + network round trips; compare
// it with the memory benchmarks only knowing the network cost.
func BenchmarkIntegrationRedis(b *testing.B) {
	url := os.Getenv("NFORGE_TEST_REDIS_URL")
	if url == "" {
		b.Skip("NFORGE_TEST_REDIS_URL not set")
	}
	c, err := Open(context.Background(), Config{Enabled: true, Prefix: "nforge-bench", Redis: RedisConfig{URL: url}},
		WithCodecRegistry(testRegistry(b)))
	if err != nil {
		b.Fatal(err)
	}
	defer c.Shutdown(context.Background())
	ctx := context.Background()
	defer c.Delete(ctx, "raw", "obj")
	_ = c.SetBytes(ctx, "raw", make([]byte, 256), 0)
	_ = c.Set(ctx, "obj", User{ID: 1, Name: "Ana"}, 0)
	b.Run("get_bytes", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := c.GetBytes(ctx, "raw"); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("get_object", func(b *testing.B) {
		b.ReportAllocs()
		var u User
		for b.Loop() {
			if err := c.Get(ctx, "obj", &u); err != nil {
				b.Fatal(err)
			}
		}
	})
}
