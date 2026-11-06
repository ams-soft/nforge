package deps

import (
	"context"
	"net"
	"runtime"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ams-soft/nforge/cache"
	"github.com/ams-soft/nforge/module"
)

// settledGoroutines waits for the count to reach want or stop changing.
func settledGoroutines(want int) int {
	got := runtime.NumGoroutine()
	for range 200 {
		if got <= want {
			return got
		}
		time.Sleep(5 * time.Millisecond)
		got = runtime.NumGoroutine()
	}
	return got
}

// A disabled module opens no connection: every backend below points at a
// listener that counts connections, and nothing may reach it.
func TestDisabledModulesOpenNoConnections(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	var accepted atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			_ = c.Close()
		}
	}()
	host, portText, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portText)

	cfg := minimalConfig()
	cfg.Database.DSN = "postgres://u:p@" + ln.Addr().String() + "/db"
	cfg.Cache = cache.Config{Driver: cache.DriverRedis}
	cfg.Cache.Redis.Host, cfg.Cache.Redis.Port = host, port
	cfg.Logging.Output.Otel.Endpoint = ln.Addr().String() // the master switch stays off
	d, err := NewDepsE(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := accepted.Load(); got != 0 {
		t.Fatalf("%d connections reached a disabled backend", got)
	}
}

// The minimal application starts no goroutine at all, and the memory cache owns
// exactly its cleanup worker until Shutdown.
func TestModulesOwnTheirGoroutines(t *testing.T) {
	before := settledGoroutines(0)

	d, err := NewDepsE(minimalConfig())
	if err != nil {
		t.Fatal(err)
	}
	if got := settledGoroutines(before); got > before {
		t.Fatalf("minimal bootstrap: %d goroutines, was %d", got, before)
	}
	if err := d.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	cfg := minimalConfig()
	cfg.Cache = cache.Config{Enabled: true, Driver: cache.DriverMemory}
	cache.ApplyDefaults(&cfg.Cache)
	d, err = NewDepsE(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := settledGoroutines(before + 1); got != before+1 {
		t.Fatalf("memory cache: %d goroutines, want %d", got, before+1)
	}
	if err := d.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := settledGoroutines(before); got > before {
		t.Fatalf("after shutdown: %d goroutines, was %d", got, before)
	}
}

// BenchmarkResolveBuiltin is the graph cost every bootstrap pays.
func BenchmarkResolveBuiltin(b *testing.B) {
	cfg := minimalConfig()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := module.Resolve(cfg, builtinModules(false)); err != nil {
			b.Fatal(err)
		}
	}
}
