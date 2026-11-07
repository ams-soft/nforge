//go:build integration

package app_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	stdhttp "net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ams-soft/nforge/app"
	"github.com/ams-soft/nforge/cache"
	"github.com/ams-soft/nforge/codec"
	"github.com/ams-soft/nforge/config"
	"github.com/ams-soft/nforge/database"
	nhttp "github.com/ams-soft/nforge/http"
	"github.com/ams-soft/nforge/internal/apptest"
	"github.com/ams-soft/nforge/jose"
	"github.com/ams-soft/nforge/logs"
	"github.com/ams-soft/nforge/module"
	"github.com/jackc/pgx/v5"
)

const integrationAppName = "nforge-app-integration"

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

func redisURL(t *testing.T) string {
	t.Helper()
	u := os.Getenv("NFORGE_TEST_REDIS_URL")
	if u == "" {
		t.Skip("NFORGE_TEST_REDIS_URL not set")
	}
	return u
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

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().(*net.TCPAddr).Port
}

func newApp(t *testing.T, yaml string, mods ...module.Descriptor) *app.Application {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(apptest.Header+yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	doc, err := config.LoadFlags(path, "")
	if err != nil {
		t.Fatal(err)
	}
	a, err := app.New(doc, app.Modules(mods...), apptest.Quiet(), app.WithProbeInterval(time.Nanosecond, 2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// A failure after the database started must close its pool: PostgreSQL itself
// reports the connections gone.
func TestIntegrationFailedStartReleasesTheDatabase(t *testing.T) {
	yaml := fmt.Sprintf("database: {enabled: true, dsn: '%s'}\ncache: {enabled: true, redis: {host: 127.0.0.1, port: 1}}\n", integrationDSN(t))
	a := newApp(t, yaml, logs.Module(), codec.Module(), database.Module(), cache.Module())
	err := a.Start(context.Background())
	var failed *module.LifecycleError
	if !errors.As(err, &failed) || failed.Module != "cache" || failed.Phase != module.PhaseInit {
		t.Fatalf("err = %v", err)
	}
	if got := waitConnections(t, 0); got != 0 {
		t.Fatalf("%d connections survived the rollback", got)
	}
}

// The full stack on the real services: one pool shared by the handlers, the
// readiness route probing PostgreSQL and Redis, a request in flight drained
// before the pool closes, and nothing left open afterwards.
func TestIntegrationFullStackLifecycle(t *testing.T) {
	port := freePort(t)
	yaml := fmt.Sprintf(`database: {enabled: true, dsn: '%s'}
cache: {enabled: true, prefix: 'nforge-it:%d', redis: {url: '%s'}}
http: {server: {host: 127.0.0.1, port: %d}, routes: {readiness: {enabled: true}}}
`, integrationDSN(t), time.Now().UnixNano(), redisURL(t), port)
	entered, release := make(chan struct{}), make(chan struct{})
	api := module.New("api", module.Definition{
		Requires: []module.Handle{nhttp.Capability, database.Capability, cache.Capability},
		Init: func(_ context.Context, sc *module.Scope) (module.Hooks, error) {
			s, db, c := module.Require(sc, nhttp.Capability), module.Require(sc, database.Capability), module.Require(sc, cache.Capability)
			s.Router().Get("/slow", func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
				close(entered)
				<-release
				var one int
				if err := db.Executor.QueryRow(r.Context(), "SELECT 1").Scan(&one); err != nil {
					stdhttp.Error(w, err.Error(), 500)
					return
				}
				if err := c.SetBytes(r.Context(), "k", []byte("v"), time.Minute); err != nil {
					stdhttp.Error(w, err.Error(), 500)
					return
				}
				_, _ = io.WriteString(w, strconv.Itoa(one))
			})
			return module.Hooks{}, nil
		},
	})
	a := newApp(t, yaml, logs.Module(), codec.Module(), database.Module(), jose.Module(), cache.Module(), nhttp.Module(), api)
	if want := []module.ID{"logger", "codec", "database", "cache", "http", "api"}; !slices.Equal(a.Graph().Order, want) {
		t.Fatalf("order = %v", a.Graph().Order)
	}
	ctx := context.Background()
	if err := a.Start(ctx); err != nil {
		t.Fatal(err)
	}
	resp, err := stdhttp.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/ready")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"database":"ready"`) || !strings.Contains(string(body), `"cache":"ready"`) {
		t.Fatalf("readiness: %d %s", resp.StatusCode, body)
	}

	result := make(chan string, 1)
	go func() {
		resp, err := stdhttp.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/slow")
		if err != nil {
			result <- err.Error()
			return
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		result <- strconv.Itoa(resp.StatusCode) + " " + string(b)
	}()
	<-entered
	stopped := make(chan error, 1)
	go func() { stopped <- a.Shutdown(ctx) }()
	time.Sleep(50 * time.Millisecond)
	close(release)
	if got := <-result; got != "200 1" {
		t.Fatalf("the request in flight got %q", got)
	}
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	if got := waitConnections(t, 0); got != 0 {
		t.Fatalf("%d connections after Shutdown", got)
	}
	c, _ := app.Value(a, cache.Capability)
	if err := c.Ping(ctx); !errors.Is(err, cache.ErrClosed) {
		t.Fatalf("cache after Shutdown: %v", err)
	}
}
