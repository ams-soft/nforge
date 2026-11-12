package nhttp

import (
	"context"
	stdhttp "net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ams-soft/nforge/cache"
	"github.com/ams-soft/nforge/codec"
)

func okHandler() stdhttp.Handler {
	return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) { w.WriteHeader(204) })
}

func limited(t *testing.T, mw func(stdhttp.Handler) stdhttp.Handler) *Server {
	t.Helper()
	s := limitsServer(t, nil)
	s.Router().With(mw).Get("/x", okHandler().ServeHTTP)
	return s
}

func hit(s *Server, remote string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", "/x", nil)
	req.RemoteAddr = remote
	return serveReq(s, req)
}

func TestRateLimitFixedWindowPerClient(t *testing.T) {
	s := limited(t, RateLimit(3, time.Minute))
	for i := range 3 {
		rec := hit(s, "10.0.0.1:1111")
		if rec.Code != 204 || rec.Header().Get("RateLimit-Limit") != "3" || rec.Header().Get("RateLimit-Remaining") != strconv.Itoa(2-i) {
			t.Fatalf("request %d: %d %v", i+1, rec.Code, rec.Header())
		}
	}
	rec := hit(s, "10.0.0.1:2222") // same IP, another port: same client
	if rec.Code != 429 || rec.Header().Get("Retry-After") == "" || rec.Header().Get("RateLimit-Remaining") != "0" {
		t.Fatalf("over the limit: %d %v", rec.Code, rec.Header())
	}
	if ra, _ := strconv.Atoi(rec.Header().Get("Retry-After")); ra < 1 || ra > 60 {
		t.Errorf("Retry-After = %q", rec.Header().Get("Retry-After"))
	}
	if rec.Header().Get("Content-Type") == "" || rec.Body.Len() == 0 {
		t.Error("429 must carry the envelope")
	}
	if other := hit(s, "10.0.0.2:1"); other.Code != 204 {
		t.Fatalf("another client must not be limited: %d", other.Code)
	}
}

func TestRateLimitWindowResets(t *testing.T) {
	c := newMemCounter()
	n1, _ := c.hit("k", 40*time.Millisecond)
	n2, _ := c.hit("k", 40*time.Millisecond)
	time.Sleep(60 * time.Millisecond)
	n3, _ := c.hit("k", 40*time.Millisecond)
	if n1 != 1 || n2 != 2 || n3 != 1 {
		t.Fatalf("counts %d %d %d, want 1 2 1", n1, n2, n3)
	}
}

func TestRateLimitIsExactUnderConcurrency(t *testing.T) {
	s := limited(t, RateLimit(100, time.Minute))
	var ok, denied atomic.Int64
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			for range 50 {
				if hit(s, "10.1.1.1:9").Code == 204 {
					ok.Add(1)
				} else {
					denied.Add(1)
				}
			}
		})
	}
	wg.Wait()
	if ok.Load() != 100 || denied.Load() != 700 {
		t.Fatalf("allowed %d denied %d, want exactly 100 and 700", ok.Load(), denied.Load())
	}
}

func TestRateLimitKeys(t *testing.T) {
	s := limited(t, RateLimit(1, time.Minute, WithRateLimitKey(KeyByHeader("X-Client"))))
	get := func(h string) int {
		req := httptest.NewRequest("GET", "/x", nil)
		req.RemoteAddr = "10.0.0.9:1"
		if h != "" {
			req.Header.Set("X-Client", h)
		}
		return serveReq(s, req).Code
	}
	if get("a") != 204 || get("a") != 429 || get("b") != 204 {
		t.Fatal("the header value selects the bucket")
	}
	if get("") != 204 || get("") != 429 { // no header: the peer address
		t.Fatal("without the header the peer address is the bucket")
	}
	skip := limited(t, RateLimit(1, time.Minute, WithRateLimitKey(func(*stdhttp.Request) (string, bool) { return "", false })))
	for range 3 {
		if hit(skip, "10.0.0.1:1").Code != 204 {
			t.Fatal("ok=false must skip the limit")
		}
	}
}

func TestRateLimitInTheCacheBackend(t *testing.T) {
	reg, _ := codec.New(codec.Config{})
	cfg := cache.Config{Enabled: true, Driver: cache.DriverMemory}
	cache.ApplyDefaults(&cfg)
	c, err := cache.Open(context.Background(), cfg, cache.WithNamespace("t", "a"), cache.WithCodecRegistry(reg))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Shutdown(context.Background()) }()
	s := limited(t, RateLimit(2, time.Minute, WithRateLimitCache(c), WithRateLimitScope("api:")))
	codes := []int{hit(s, "10.0.0.1:1").Code, hit(s, "10.0.0.1:1").Code, hit(s, "10.0.0.1:1").Code}
	if codes[0] != 204 || codes[1] != 204 || codes[2] != 429 {
		t.Fatalf("codes %v", codes)
	}
	// a failing backend lets the request through by default and refuses it when fail-closed
	_ = c.Shutdown(context.Background())
	if hit(s, "10.0.0.3:1").Code != 204 {
		t.Error("default: fail open")
	}
	closed := limited(t, RateLimit(2, time.Minute, WithRateLimitCache(c), WithRateLimitFailClosed()))
	if hit(closed, "10.0.0.3:1").Code != 503 {
		t.Error("fail closed must answer 503")
	}
}

func TestRateLimitConstructionPanics(t *testing.T) {
	for name, f := range map[string]func(){
		"zero limit":   func() { RateLimit(0, time.Minute) },
		"short window": func() { RateLimit(1, time.Millisecond) },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s must panic", name)
				}
			}()
			f()
		}()
	}
}

// Load shedding: requests beyond max_inflight_requests get 503, and a stream
// that called BeginStream gives its slot back.
func TestMaxInflightRequestsShedsLoad(t *testing.T) {
	s := limitsServer(t, func(c *testConfig) { c.HTTP.Server.MaxInflightRequests = 2 })
	gate := make(chan struct{})
	started := make(chan struct{}, 8)
	s.Router().Get("/slow", func(w stdhttp.ResponseWriter, r *stdhttp.Request) { started <- struct{}{}; <-gate; w.WriteHeader(204) })
	s.Router().Get("/stream", func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		BeginStream(w, r)
		started <- struct{}{}
		<-gate
		w.WriteHeader(204)
	})
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() { serveReq(s, httptest.NewRequest("GET", "/slow", nil)) })
	}
	<-started
	<-started
	rec := serveReq(s, httptest.NewRequest("GET", "/slow", nil))
	if rec.Code != 503 || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("third request: %d %v", rec.Code, rec.Header())
	}
	close(gate)
	wg.Wait()

	// two streams take no slots
	gate2 := make(chan struct{})
	gate = gate2
	for range 2 {
		wg.Go(func() { serveReq(s, httptest.NewRequest("GET", "/stream", nil)) })
	}
	<-started
	<-started
	for range 2 { // both ordinary slots are free while two streams run
		if rec := serveReq(s, httptest.NewRequest("GET", "/nothing", nil)); rec.Code == 503 {
			t.Fatal("streams must not hold request slots")
		}
	}
	close(gate2)
	wg.Wait()
}
