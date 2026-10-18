package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ams-soft/nforge/cache"
)

func TestCacheDisabledByDefault(t *testing.T) {
	cfg, err := loadTenantConfig(t, "http: {server: {port: 8080}}", "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Cache.Enabled || cfg.Cache.Driver != cache.DriverRedis || cfg.Cache.Codec != cache.CodecJSON {
		t.Fatalf("cache = %+v", cfg.Cache)
	}
}

func TestCacheYAMLWithEnvExpansion(t *testing.T) {
	t.Setenv("TEST_REDIS_PASSWORD", "s3cr3t")
	yamlText := `
http: {server: {port: 8080}}
codec: {formats: {enabled: [json, cbor]}}
cache:
  enabled: true
  driver: redis
  codec: cbor
  prefix: my-app:v2
  limits: {max_key_bytes: 512, max_value_bytes: 2mb, max_inflight_loads: 64}
  load_timeout: 3s
  redis:
    host: redis.internal
    port: 6380
    password: ${TEST_REDIS_PASSWORD}
    database: 2
    retry: {read_retries: 0}
`
	cfg, err := loadTenantConfig(t, yamlText, "")
	if err != nil {
		t.Fatal(err)
	}
	c := cfg.Cache
	if !c.Enabled || c.Codec != "cbor" || c.Prefix != "my-app:v2" || c.LoadTimeout != 3*time.Second {
		t.Fatalf("cache = %+v", c)
	}
	if c.Limits.MaxKeyBytes != 512 || c.Limits.MaxValueBytes != 2*cache.MB || c.Limits.MaxInflightLoads != 64 {
		t.Fatalf("limits = %+v", c.Limits)
	}
	if c.Redis.Host != "redis.internal" || c.Redis.Port != 6380 || c.Redis.Password != "s3cr3t" || c.Redis.Database != 2 {
		t.Fatal("redis address/credentials not loaded")
	}
	if *c.Redis.Retry.ReadRetries != 0 {
		t.Fatalf("explicit read_retries: 0 must be kept, got %d", *c.Redis.Retry.ReadRetries)
	}
	if c.Redis.Pool.Size != 32 || c.Memory.MaxEntries != 10000 {
		t.Fatal("defaults not applied")
	}
}

func TestCacheEnvFile(t *testing.T) {
	envText := `CACHE_ENABLED=true
CACHE_DRIVER=memory
CACHE_LIMITS_MAX_VALUE_BYTES=512kb
CACHE_MEMORY_MAX_ENTRIES=50
CACHE_MEMORY_MAX_BYTES=8mb
CACHE_MEMORY_CLEANUP_INTERVAL=5s
CACHE_REDIS_RETRY_READ_RETRIES=2
CACHE_REDIS_TIMEOUTS_OPERATION=750ms
`
	cfg, err := loadTenantConfig(t, "http: {server: {port: 8080}}\ncache: {enabled: false, driver: redis}", envText)
	if err != nil {
		t.Fatal(err)
	}
	c := cfg.Cache
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

func TestCacheOverlay(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir) // overlays resolve from the working directory
	base := "env: dev\napp: {name: t, version: 1.0.0}\nhttp: {server: {port: 8080}}\ncache: {enabled: true, driver: memory}\n"
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(base), 0o600); err != nil {
		t.Fatal(err)
	}
	overlay := "cache: {driver: redis, redis: {url: 'rediss://cache.prod:6380/1'}}\n"
	if err := os.WriteFile(filepath.Join(dir, "config.dev.yaml"), []byte(overlay), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFlags("", "")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Cache.Enabled || cfg.Cache.Driver != "redis" || cfg.Cache.Redis.URL != "rediss://cache.prod:6380/1" {
		t.Fatalf("overlay not applied: %+v", cfg.Cache)
	}
}

func TestCacheValidationAtLoad(t *testing.T) {
	cases := []struct {
		name, yaml string
		want       error
	}{
		{"unknown driver", "cache: {enabled: true, driver: memcached}", cache.ErrUnsupportedDriver},
		{"codec not enabled", "cache: {enabled: true, codec: cbor}", cache.ErrUnsupportedCodec},
		{"xml codec", "codec: {formats: {enabled: [json, xml]}}\ncache: {enabled: true, codec: xml}", cache.ErrUnsupportedCodec},
		{"negative port", "cache: {enabled: true, redis: {port: -1}}", cache.ErrInvalidConfig},
		{"url query", "cache: {enabled: true, redis: {url: 'redis://h/0?pool_size=3'}}", cache.ErrInvalidConfig},
		{"disabled is not validated", "cache: {enabled: false, driver: memcached}", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadTenantConfig(t, "http: {server: {port: 8080}}\n"+tc.yaml, "")
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
}
