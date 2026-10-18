package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ams-soft/nforge/config"
	"github.com/ams-soft/nforge/deps"
	nhttp "github.com/ams-soft/nforge/http"
)

// newTestServer loads the real config.yaml (quiet) with extra env-file
// lines and returns the server and its catalog.
func newTestServer(t *testing.T, env string) (*nhttp.ServerHttp, *catalog) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "test.env")
	content := "LOGGING_OUTPUT_TERMINAL_ENABLED=false\nHTTP_SERVER_HEADER=false\n" + env
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFlags("config.yaml", file)
	if err != nil {
		t.Fatal(err)
	}
	dps := deps.NewDeps(*cfg)
	t.Cleanup(func() { _ = dps.Shutdown(context.Background()) })
	products := newCatalog()
	return newServer(dps, products), products
}

func memoryServer(t *testing.T) (*nhttp.ServerHttp, *catalog) {
	t.Helper()
	s, c := newTestServer(t, "CACHE_DRIVER=memory\n")
	if s.Deps.Cache.Driver() != "memory" {
		t.Fatal("the test env selects the memory driver")
	}
	return s, c
}

type result struct {
	status int
	data   map[string]any
	raw    string
}

func call(t *testing.T, s *nhttp.ServerHttp, method, path, body string) result {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" && !strings.HasPrefix(path, "/raw/") {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	s.Srv.Handler.ServeHTTP(rec, req)
	raw, _ := io.ReadAll(rec.Body)
	var env struct {
		Data map[string]any `json:"data"`
	}
	_ = json.Unmarshal(raw, &env)
	return result{rec.Code, env.Data, string(raw)}
}

// exerciseObjects covers Set/Get/TTL/Expire/SetIfAbsent/Delete/Increment and
// the raw view of an object; it runs on both drivers.
func exerciseObjects(t *testing.T, s *nhttp.ServerHttp) {
	if r := call(t, s, "GET", "/api/v1/items/greeting", ""); r.status != 404 {
		t.Fatalf("miss: %d %s", r.status, r.raw)
	}
	if r := call(t, s, "PUT", "/api/v1/items/greeting", `{"name":"hello","ttl":"1m"}`); r.status != 204 {
		t.Fatalf("put: %d %s", r.status, r.raw)
	}
	if r := call(t, s, "GET", "/api/v1/items/greeting", ""); r.status != 200 || r.data["name"] != "hello" {
		t.Fatalf("get: %d %s", r.status, r.raw)
	}
	// stored as plain JSON (no envelope), visible through the raw route
	if r := call(t, s, "GET", "/raw/items:greeting", ""); r.status != 200 || r.raw != `{"name":"hello"}` {
		t.Fatalf("raw view: %d %s", r.status, r.raw)
	}
	ttl := call(t, s, "GET", "/api/v1/items/greeting/ttl", "").data
	if ttl["exists"] != true || ttl["persistent"] != false || ttl["remaining_ms"].(float64) <= 0 {
		t.Fatalf("ttl: %v", ttl)
	}
	if r := call(t, s, "POST", "/api/v1/items/greeting/expire", `{"ttl":"5s"}`); r.data["updated"] != true {
		t.Fatalf("expire: %s", r.raw)
	}
	if r := call(t, s, "POST", "/api/v1/items/greeting/expire", `{"ttl":"0s"}`); r.status != 400 {
		t.Fatalf("expire 0: %d", r.status)
	}
	if r := call(t, s, "PUT", "/api/v1/items/greeting", `{"name":"x","ttl":"soon"}`); r.status != 400 {
		t.Fatalf("bad ttl: %d", r.status)
	}

	// NX: only the first claim wins
	if r := call(t, s, "POST", "/api/v1/items/lock/claim", `{"name":"worker-1","ttl":"10s"}`); r.data["created"] != true {
		t.Fatalf("claim 1: %s", r.raw)
	}
	if r := call(t, s, "POST", "/api/v1/items/lock/claim", `{"name":"worker-2"}`); r.data["created"] != false {
		t.Fatalf("claim 2: %s", r.raw)
	}

	if r := call(t, s, "DELETE", "/api/v1/items/greeting", ""); r.data["deleted"] != float64(1) {
		t.Fatalf("delete: %s", r.raw)
	}
	if r := call(t, s, "DELETE", "/api/v1/items/greeting", ""); r.data["deleted"] != float64(0) {
		t.Fatalf("delete again: %s", r.raw)
	}
	if ttl := call(t, s, "GET", "/api/v1/items/greeting/ttl", "").data; ttl["exists"] != false {
		t.Fatalf("ttl after delete: %v", ttl)
	}

	for i, want := range []float64{1, 2, -8} {
		delta := []string{"1", "1", "-10"}[i]
		if r := call(t, s, "POST", "/api/v1/counters/visits", `{"delta":`+delta+`}`); r.data["value"] != want {
			t.Fatalf("counter step %d: %s", i, r.raw)
		}
	}
	// an object is not a counter
	if r := call(t, s, "PUT", "/raw/counters:obj", `{"name":"x"}`); r.status != 204 {
		t.Fatalf("raw put: %d", r.status)
	}
	if r := call(t, s, "POST", "/api/v1/counters/obj", `{"delta":1}`); r.status != 409 {
		t.Fatalf("non-integer counter: %d", r.status)
	}

	if r := call(t, s, "PUT", "/raw/blob?ttl=1m", "bytes\x00here"); r.status != 204 {
		t.Fatalf("raw put: %d", r.status)
	}
	if r := call(t, s, "GET", "/raw/blob", ""); r.status != 200 || r.raw != "bytes\x00here" {
		t.Fatalf("raw get: %d %q", r.status, r.raw)
	}
}

// exerciseGetOrLoad shows that concurrent misses share one load and that
// loader errors are not cached.
func exerciseGetOrLoad(t *testing.T, s *nhttp.ServerHttp, products *catalog) {
	products.delay = 200 * time.Millisecond
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			if r := call(t, s, "GET", "/api/v1/products/1", ""); r.status != 200 || r.data["name"] != "Keyboard" {
				t.Errorf("product 1: %d %s", r.status, r.raw)
			}
		})
	}
	wg.Wait()
	if n := products.loads.Load(); n != 1 {
		t.Fatalf("10 concurrent misses ran the loader %d times, want 1", n)
	}
	if r := call(t, s, "GET", "/api/v1/products/1", ""); r.status != 200 || products.loads.Load() != 1 {
		t.Fatalf("hit: %d, loads %d", r.status, products.loads.Load())
	}
	for range 2 {
		if r := call(t, s, "GET", "/api/v1/products/99", ""); r.status != 404 {
			t.Fatalf("missing product: %d", r.status)
		}
	}
	if n := products.loads.Load(); n != 3 {
		t.Fatalf("loads = %d, want 3 (errors are not cached)", n)
	}
}

func TestObjectsAndMetadata(t *testing.T) {
	s, _ := memoryServer(t)
	exerciseObjects(t, s)
}

func TestGetOrLoadSharesOneLoad(t *testing.T) {
	s, products := memoryServer(t)
	exerciseGetOrLoad(t, s, products)
}

func TestReadinessReportsCache(t *testing.T) {
	s, _ := memoryServer(t)
	r := call(t, s, "GET", "/ready", "")
	if c, _ := r.data["cache"].(map[string]any); r.status != 200 || c["health"] != true {
		t.Fatalf("ready: %d %s", r.status, r.raw)
	}
}

func TestCBORObjects(t *testing.T) {
	s, _ := newTestServer(t, "CACHE_DRIVER=memory\nCACHE_CODEC=cbor\n")
	if r := call(t, s, "PUT", "/api/v1/items/greeting", `{"name":"hello"}`); r.status != 204 {
		t.Fatalf("put: %d", r.status)
	}
	// the stored bytes are CBOR: a one-entry map with the key "name"
	if r := call(t, s, "GET", "/raw/items:greeting", ""); !strings.HasPrefix(r.raw, "\xa1\x64name") {
		t.Fatalf("raw: %q", r.raw)
	}
	if r := call(t, s, "GET", "/api/v1/items/greeting", ""); r.data["name"] != "hello" {
		t.Fatalf("get: %s", r.raw)
	}
}
