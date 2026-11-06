//go:build integration

package deps

import (
	"context"
	"errors"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/ams-soft/nforge/cache"
	"github.com/ams-soft/nforge/module"
	"github.com/jackc/pgx/v5"
)

const integrationAppName = "nforge-deps-integration"

func integrationDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("NFORGE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("NFORGE_TEST_DATABASE_URL not set")
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("application_name", integrationAppName)
	u.RawQuery = q.Encode()
	return u.String()
}

// openConnections counts the server's view of the connections of this test.
func openConnections(t *testing.T) int {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, os.Getenv("NFORGE_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	var n int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM pg_stat_activity WHERE application_name = $1", integrationAppName).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func waitConnections(t *testing.T, want int) int {
	t.Helper()
	got := openConnections(t)
	for deadline := time.Now().Add(3 * time.Second); got != want && time.Now().Before(deadline); {
		time.Sleep(20 * time.Millisecond)
		got = openConnections(t)
	}
	return got
}

// A failure after the database started must close its pool: PostgreSQL itself
// reports the connections gone.
func TestIntegrationFailedStartReleasesTheDatabase(t *testing.T) {
	cfg := minimalConfig()
	cfg.Database.Enabled, cfg.Database.DSN = true, integrationDSN(t)
	cfg.Cache = cache.Config{Enabled: true, Driver: cache.DriverRedis}
	cache.ApplyDefaults(&cfg.Cache)
	cfg.Cache.Redis.Host, cfg.Cache.Redis.Port = "127.0.0.1", 1 // nothing listens here

	_, err := NewDepsE(cfg)
	var failed *module.InitError
	if !errors.As(err, &failed) || failed.Module != "cache" {
		t.Fatalf("err = %v", err)
	}
	if got := waitConnections(t, 0); got != 0 {
		t.Fatalf("%d connections survived the rollback", got)
	}
}

// One pool, shared and closed once; a module asking for the database receives
// that pool and adds no connection of its own.
func TestIntegrationFullStackLifecycle(t *testing.T) {
	redisURL := os.Getenv("NFORGE_TEST_REDIS_URL")
	if redisURL == "" {
		t.Skip("NFORGE_TEST_REDIS_URL not set")
	}
	cfg := minimalConfig()
	cfg.Database.Enabled, cfg.Database.DSN = true, integrationDSN(t)
	cfg.Cache = cache.Config{Enabled: true, Driver: cache.DriverRedis}
	cfg.Cache.Redis.URL = redisURL
	cache.ApplyDefaults(&cfg.Cache)

	d, err := NewDepsE(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, s := range d.ModuleStatus(ctx) {
		if s.ID != "jose" && s.ID != "tracing" && s.Availability != module.Ready {
			t.Errorf("%s = %+v", s.ID, s)
		}
	}
	if got := waitConnections(t, 1); got != 1 {
		t.Errorf("%d database connections after bootstrap, want the one startup ping", got)
	}
	if err := d.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if got := waitConnections(t, 0); got != 0 {
		t.Fatalf("%d connections after Shutdown", got)
	}
	if err := d.PingCache(ctx); !errors.Is(err, cache.ErrClosed) {
		t.Fatalf("cache after Shutdown: %v", err)
	}
}
