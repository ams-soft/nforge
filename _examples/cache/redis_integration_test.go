//go:build integration

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"

	nhttp "github.com/ams-soft/nforge/http"
)

// redisServer runs the example on a real, disposable Redis under a unique
// prefix, and deletes only its own keys afterwards (never FLUSHDB):
//
//	NFORGE_TEST_REDIS_URL=redis://localhost:6379/15 go test -tags integration ./_examples/cache
func redisServer(t *testing.T) (*nhttp.ServerHttp, *catalog) {
	t.Helper()
	url := os.Getenv("NFORGE_TEST_REDIS_URL")
	if url == "" {
		t.Skip("NFORGE_TEST_REDIS_URL not set")
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	s, products := newTestServer(t, "CACHE_REDIS_URL="+url+"\nCACHE_PREFIX=nforge-test:"+hex.EncodeToString(b)+"\n")
	if s.Deps.Cache.Driver() != "redis" {
		t.Fatal("expected the redis driver")
	}
	t.Cleanup(func() {
		_, _ = s.Deps.Cache.Delete(context.Background(),
			"items:greeting", "items:lock", "counters:visits", "counters:obj", "blob", "products:1")
	})
	return s, products
}

func TestIntegrationRedisObjects(t *testing.T) {
	s, _ := redisServer(t)
	exerciseObjects(t, s)
}

func TestIntegrationRedisGetOrLoad(t *testing.T) {
	s, products := redisServer(t)
	exerciseGetOrLoad(t, s, products)
}
