package app_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"runtime"
	"testing"
	"time"

	"github.com/ams-soft/nforge/cache"
	"github.com/ams-soft/nforge/codec"
	nhttp "github.com/ams-soft/nforge/http"
	"github.com/ams-soft/nforge/internal/apptest"
	"github.com/ams-soft/nforge/logs"
	"github.com/ams-soft/nforge/metrics"
	"github.com/ams-soft/nforge/sse"
)

func settledGoroutines() int {
	var n int
	for range 20 {
		runtime.GC()
		n = runtime.NumGoroutine()
		time.Sleep(25 * time.Millisecond)
		if runtime.NumGoroutine() == n {
			return n
		}
	}
	return n
}

// Full start/stop cycles of an application (HTTP, codec, memory cache, metrics, SSE) leave
// nothing behind: goroutines, and the port, come back every time.
func TestStartStopCyclesLeaveNothingBehind(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	yaml := fmt.Sprintf(`http:
  server: {host: 127.0.0.1, port: %d}
  routes: {readiness: {enabled: true}, metrics: {enabled: true}}
cache: {enabled: true, driver: memory, memory: {cleanup_interval: 1s}}
sse: {heartbeat: 1s}
`, port)
	cycle := func() {
		a, err := apptest.New(t, yaml, logs.Module(), codec.Module(), metrics.Module(), cache.Module(), nhttp.Module(), sse.Module())
		if err != nil {
			t.Fatal(err)
		}
		if err := a.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/ready", port))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		http.DefaultClient.CloseIdleConnections()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := a.Shutdown(ctx); err != nil {
			t.Fatal(err)
		}
	}
	for range 3 {
		cycle()
	}
	baseline := settledGoroutines()
	for range 40 {
		cycle()
	}
	if after := settledGoroutines(); after > baseline+3 {
		buf := make([]byte, 1<<16)
		t.Fatalf("goroutines %d -> %d after 40 cycles:\n%s", baseline, after, buf[:runtime.Stack(buf, true)])
	}
}
