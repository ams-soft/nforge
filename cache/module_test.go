package cache_test

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ams-soft/nforge/app"
	"github.com/ams-soft/nforge/cache"
	"github.com/ams-soft/nforge/codec"
	"github.com/ams-soft/nforge/config"
	"github.com/ams-soft/nforge/database"
	"github.com/ams-soft/nforge/internal/apptest"
	"github.com/ams-soft/nforge/module"
)

// readCache captures the validated cache configuration through a dependent's
// Check, as any module that declares the cache sees it.
func readCache(got *cache.Config) module.Descriptor {
	return module.New("reader", module.Definition{Optional: []module.Handle{cache.Capability}, Check: func(v *module.View) error {
		cfg, _, _, err := module.ConfigOf[cache.Config](v, cache.Capability)
		*got = cfg
		return err
	}})
}

func newApp(t *testing.T, yaml, env string, extra ...module.Descriptor) (*app.Application, error) {
	t.Helper()
	d, err := apptest.LoadE(t, yaml, env)
	if err != nil {
		return nil, err
	}
	return app.New(d, app.Modules(append([]module.Descriptor{codec.Module(), cache.Module()}, extra...)...), apptest.Quiet())
}

func TestModuleDisabledByDefault(t *testing.T) {
	var got cache.Config
	a, err := newApp(t, "", "", readCache(&got))
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled || got.Driver != cache.DriverRedis || got.Codec != cache.CodecJSON {
		t.Fatalf("cache = %+v", got)
	}
	if err := a.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Shutdown(context.Background()) }()
	if _, ok := app.Value(a, cache.Capability); ok {
		t.Fatal("a disabled cache publishes nothing")
	}
}

func TestModuleYAMLWithEnvExpansion(t *testing.T) {
	t.Setenv("TEST_REDIS_PASSWORD", "s3cr3t")
	yaml := `
codec: {formats: {enabled: [json, cbor]}}
cache:
  enabled: true
  driver: redis
  codec: cbor
  prefix: my-app:v2
  limits: {max_key_bytes: 512, max_value_bytes: 2mb, max_inflight_loads: 64}
  load_timeout: 3s
  redis:
    host: localhost
    port: 6380
    password: ${TEST_REDIS_PASSWORD}
    database: 2
    retry: {read_retries: 0}
`
	var c cache.Config
	if _, err := newApp(t, yaml, "", readCache(&c)); err != nil {
		t.Fatal(err)
	}
	if !c.Enabled || c.Codec != "cbor" || c.Prefix != "my-app:v2" || c.LoadTimeout != 3*time.Second {
		t.Fatalf("cache = %+v", c)
	}
	if c.Limits.MaxKeyBytes != 512 || c.Limits.MaxValueBytes != 2*cache.MB || c.Limits.MaxInflightLoads != 64 {
		t.Fatalf("limits = %+v", c.Limits)
	}
	if c.Redis.Port != 6380 || c.Redis.Password != "s3cr3t" || c.Redis.Database != 2 {
		t.Fatal("redis address/credentials not loaded")
	}
	if *c.Redis.Retry.ReadRetries != 0 {
		t.Fatalf("explicit read_retries: 0 must be kept, got %d", *c.Redis.Retry.ReadRetries)
	}
	if c.Redis.Pool.Size != 32 || c.Memory.MaxEntries != 10000 {
		t.Fatal("defaults not applied")
	}
}

func TestModuleEnvFile(t *testing.T) {
	env := `CACHE_ENABLED=true
CACHE_DRIVER=memory
CACHE_LIMITS_MAX_VALUE_BYTES=512kb
CACHE_MEMORY_MAX_ENTRIES=50
CACHE_MEMORY_MAX_BYTES=8mb
CACHE_MEMORY_CLEANUP_INTERVAL=5s
CACHE_REDIS_RETRY_READ_RETRIES=2
CACHE_REDIS_TIMEOUTS_OPERATION=750ms
`
	var c cache.Config
	if _, err := newApp(t, "cache: {enabled: false, driver: redis}\n", env, readCache(&c)); err != nil {
		t.Fatal(err)
	}
	if !c.Enabled || c.Driver != "memory" || c.Limits.MaxValueBytes != 512*cache.KB {
		t.Fatalf("cache = %+v", c)
	}
	if c.Memory.MaxEntries != 50 || c.Memory.MaxBytes != 8*cache.MB || c.Memory.CleanupInterval != 5*time.Second {
		t.Fatalf("memory = %+v", c.Memory)
	}
	if *c.Redis.Retry.ReadRetries != 2 || c.Redis.Timeouts.Operation != 750*time.Millisecond {
		t.Fatal("redis scalar leaves from the env file")
	}
}

func TestModuleOverlay(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir) // overlays resolve from the working directory
	base := "env: dev\napp: {name: t, version: 1.0.0}\ncache: {enabled: true, driver: memory}\n"
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(base), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.dev.yaml"), []byte("cache: {driver: redis, redis: {url: 'rediss://cache.prod:6380/1'}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := config.LoadFlags("", "")
	if err != nil {
		t.Fatal(err)
	}
	var c cache.Config
	if _, err := app.New(d, app.Modules(codec.Module(), cache.Module(), readCache(&c)), apptest.Quiet()); err != nil {
		t.Fatal(err)
	}
	if !c.Enabled || c.Driver != "redis" || c.Redis.URL != "rediss://cache.prod:6380/1" {
		t.Fatalf("overlay not applied: %+v", c)
	}
}

func TestModuleValidation(t *testing.T) {
	cases := []struct {
		name, yaml string
		want       error
	}{
		{"unknown driver", "cache: {enabled: true, driver: memcached}", cache.ErrUnsupportedDriver},
		{"codec not enabled", "cache: {enabled: true, codec: cbor}", cache.ErrUnsupportedCodec},
		{"xml codec", "codec: {formats: {enabled: [json, xml]}}\ncache: {enabled: true, codec: xml}", cache.ErrUnsupportedCodec},
		{"negative port", "cache: {enabled: true, redis: {port: -1}}", cache.ErrInvalidConfig},
		{"url query", "cache: {enabled: true, redis: {url: 'redis://h/0?pool_size=3'}}", cache.ErrInvalidConfig},
		{"value limit above the codec limit", "cache: {enabled: true, limits: {max_value_bytes: 20mb}}", cache.ErrInvalidConfig},
		{"value limit equal to the codec limit", "codec: {max_payload_bytes: 20mb}\ncache: {enabled: true, limits: {max_value_bytes: 20mb}}", nil},
		{"disabled is not validated", "cache: {enabled: false, driver: memcached}", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := newApp(t, tc.yaml+"\n", "")
			if tc.want == nil {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
	// the cache cannot run without the codec module
	d := apptest.Load(t, "cache: {enabled: true, driver: memory}\n", "")
	if _, err := app.New(d, app.Modules(cache.Module()), apptest.Quiet()); !errors.Is(err, module.ErrMissingDependency) {
		t.Fatalf("without codec: err = %v", err)
	}
}

func TestModuleRedisCredentialWarning(t *testing.T) {
	remote := "cache: {enabled: true, redis: {host: redis.internal, password: s3cr3t}}\n"
	a, err := newApp(t, remote, "")
	if err != nil {
		t.Fatal(err)
	}
	if w := a.Warnings(); len(w) != 1 || !strings.Contains(w[0], "without TLS") || strings.Contains(w[0], "s3cr3t") {
		t.Fatalf("warnings = %v", w)
	}
	if _, err := newApp(t, "env: prd\n"+remote, ""); err == nil || !strings.Contains(err.Error(), "prd_exceptions.redis_plaintext") {
		t.Fatalf("prd: err = %v", err)
	}
	if _, err := newApp(t, "env: prd\nprd_exceptions: {redis_plaintext: true}\n"+remote, ""); err != nil {
		t.Fatalf("accepted in prd: %v", err)
	}
	for _, safe := range []string{
		"cache: {enabled: true, redis: {host: localhost, password: x}}\n",
		"cache: {enabled: true, redis: {url: 'rediss://:x@redis.internal:6380/0'}}\n",
		"cache: {enabled: true, redis: {host: redis.internal}}\n",
		"cache: {enabled: true, driver: memory}\n",
	} {
		a, err := newApp(t, safe, "")
		if err != nil || len(a.Warnings()) != 0 {
			t.Errorf("%s: %v %v", safe, err, a.Warnings())
		}
	}
}

// The memory driver starts and is namespaced by env and app name; with
// database.tenancy enabled the keys need a tenant, even when the database
// itself is disabled.
func TestModuleTenantIsolationFollowsDatabaseTenancy(t *testing.T) {
	a := apptest.Start(t, "cache: {enabled: true, driver: memory}\n", codec.Module(), cache.Module(), database.Module())
	c, ok := app.Value(a, cache.Capability)
	if !ok || c.Driver() != cache.DriverMemory {
		t.Fatal("cache not published")
	}
	if err := c.SetBytes(context.Background(), "k", []byte("v"), 0); err != nil {
		t.Fatalf("no tenancy: %v", err)
	}

	a = apptest.Start(t, "cache: {enabled: true, driver: memory}\ndatabase: {enabled: false, tenancy: {enabled: true, mode: column}}\n",
		codec.Module(), cache.Module(), database.Module())
	c, _ = app.Value(a, cache.Capability)
	if err := c.SetBytes(context.Background(), "k", []byte("v"), 0); !errors.Is(err, cache.ErrTenantRequired) {
		t.Fatalf("without a tenant: err = %v", err)
	}
	ctx, err := database.WithTenant(context.Background(), "6f1c1a52-8a4e-4f7a-9b8e-2b1c3d4e5f60")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SetBytes(ctx, "k", []byte("v"), 0); err != nil {
		t.Fatalf("with a tenant: %v", err)
	}
}

// Redis down at startup is fatal and nothing stays open.
func TestModuleRedisDownFailsTheStart(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	a, err := newApp(t, "cache: {enabled: true, redis: {host: 127.0.0.1, port: "+itoa(port)+", timeouts: {dial: 200ms}}}\n", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "unable to setup cache") {
		t.Fatalf("err = %v", err)
	}
}

func itoa(n int) string {
	var b [20]byte
	i := len(b)
	for {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
		if n == 0 {
			return string(b[i:])
		}
	}
}
