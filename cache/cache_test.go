package cache

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ams-soft/nforge/codec"
)

func TestObjectRoundTripJSONAndCBOR(t *testing.T) {
	for _, format := range []string{CodecJSON, CodecCBOR} {
		t.Run(format, func(t *testing.T) {
			c, _ := openMemory(t, func(cfg *Config) { cfg.Codec = format })
			in := User{ID: 7, Name: "Ana", Age: 30}
			if err := c.Set(bg, "u", in, 0); err != nil {
				t.Fatal(err)
			}
			var out User
			if err := c.Get(bg, "u", &out); err != nil || out != in {
				t.Fatalf("get: %+v %v", out, err)
			}
			raw, _ := c.GetBytes(bg, "u")
			if format == CodecJSON && string(raw) != `{"id":7,"name":"Ana","age":30}` {
				t.Fatalf("json must be stored without an envelope: %s", raw)
			}
			if format == CodecCBOR && raw[0] != 0xa3 { // CBOR map(3)
				t.Fatalf("cbor payload: %x", raw)
			}
		})
	}
}

func TestRawBytesIgnoreCodec(t *testing.T) {
	c, _ := openMemory(t, func(cfg *Config) { cfg.Codec = CodecCBOR })
	_ = c.SetBytes(bg, "raw", []byte(`{"not":"cbor"}`), 0)
	if got, _ := c.GetBytes(bg, "raw"); string(got) != `{"not":"cbor"}` {
		t.Fatalf("raw = %s", got)
	}
}

func TestGetLeavesTargetUnchanged(t *testing.T) {
	c, _ := openMemory(t, nil)
	orig := User{ID: 1, Name: "keep"}

	cases := []struct {
		name  string
		setup func()
		want  error
	}{
		{"miss", func() {}, ErrNotFound},
		{"syntax", func() { _ = c.SetBytes(bg, "k", []byte("{broken"), 0) }, ErrDecode},
		{"missing required", func() { _ = c.SetBytes(bg, "k", []byte(`{"id":2}`), 0) }, ErrDecode},
		{"validation", func() { _ = c.SetBytes(bg, "k", []byte(`{"id":2,"name":"x","age":-1}`), 0) }, ErrDecode},
		{"type mismatch", func() { _ = c.SetBytes(bg, "k", []byte(`{"id":"two","name":"x"}`), 0) }, ErrDecode},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _ = c.Delete(bg, "k")
			tc.setup()
			target := orig
			err := c.Get(bg, "k", &target)
			mustIs(t, err, tc.want)
			if target != orig {
				t.Fatalf("target changed: %+v", target)
			}
		})
	}
	// codec causes stay inspectable
	_ = c.SetBytes(bg, "k", []byte(`{"id":2,"name":"x","age":-1}`), 0)
	var u User
	err := c.Get(bg, "k", &u)
	if !errors.Is(err, codec.ErrValidation) {
		t.Fatalf("validation cause lost: %v", err)
	}
}

func TestInvalidTargets(t *testing.T) {
	c, _ := openMemory(t, nil)
	_ = c.Set(bg, "k", User{Name: "x"}, 0)
	var u User
	var nilPtr *User
	for _, target := range []any{nil, u, nilPtr} {
		mustIs(t, c.Get(bg, "k", target), ErrInvalidTarget)
		mustIs(t, c.GetOrLoad(bg, "k", target, 0, func(context.Context) (any, error) { return nil, nil }), ErrInvalidTarget)
	}
}

func TestSetEncodeAndSizeErrors(t *testing.T) {
	c, _ := openMemory(t, func(cfg *Config) { cfg.Limits.MaxValueBytes = 32 })
	mustIs(t, c.Set(bg, "k", make(chan int), 0), ErrEncode)
	mustIs(t, c.Set(bg, "k", strings.Repeat("x", 64), 0), ErrValueTooLarge)
	mustIs(t, c.SetBytes(bg, "k", make([]byte, 33), 0), ErrValueTooLarge)
	if _, err := c.SetIfAbsent(bg, "k", strings.Repeat("x", 64), 0); !errors.Is(err, ErrValueTooLarge) {
		t.Fatal(err)
	}
	if ok, _ := c.Exists(bg, "k"); ok {
		t.Fatal("nothing must have been stored")
	}
}

func TestSetIfAbsentObject(t *testing.T) {
	c, _ := openMemory(t, nil)
	if ok, err := c.SetIfAbsent(bg, "u", User{Name: "a"}, 0); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if ok, err := c.SetIfAbsent(bg, "u", User{Name: "b"}, 0); ok || err != nil {
		t.Fatal(ok, err)
	}
	var u User
	_ = c.Get(bg, "u", &u)
	if u.Name != "a" {
		t.Fatalf("name = %q", u.Name)
	}
}

func TestKeyValidation(t *testing.T) {
	c, _ := openMemory(t, func(cfg *Config) { cfg.Limits.MaxKeyBytes = 32 })
	mustIs(t, c.SetBytes(bg, "", nil, 0), ErrInvalidKey)
	// "nforge:test:app:" is 16 bytes: 16 more fit, 17 do not
	if err := c.SetBytes(bg, strings.Repeat("k", 16), nil, 0); err != nil {
		t.Fatal(err)
	}
	mustIs(t, c.SetBytes(bg, strings.Repeat("k", 17), nil, 0), ErrInvalidKey)
	// keys are not trimmed and wildcards are literal
	_ = c.SetBytes(bg, " k*", []byte("v"), 0)
	if ok, _ := c.Exists(bg, "k*"); ok {
		t.Fatal("key was trimmed")
	}
	if n, _ := c.Delete(bg, "*"); n != 0 {
		t.Fatal("wildcard deleted something")
	}
}

func TestDefaultAndCustomPrefix(t *testing.T) {
	cases := []struct {
		name, prefix string
		tenant       bool
		want         string
	}{
		{"default", "", false, "nforge:local:my-app:k"},
		{"default tenant", "", true, "nforge:local:" + tenantA + ":my-app:k"},
		{"custom", "my-prefix", false, "my-prefix:k"},
		{"custom with colon", "my-prefix:", false, "my-prefix:k"},
		{"custom tenant", "my-prefix", true, "my-prefix:" + tenantA + ":k"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := []Option{WithNamespace("local", "my-app")}
			if tc.tenant {
				opts = append(opts, WithTenantResolver(tenantFrom))
			}
			cfg := memoryConfig()
			cfg.Prefix = tc.prefix
			c, err := Open(bg, cfg, opts...)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = c.Shutdown(bg) }()
			ctx := withTenant(bg, tenantA)
			_ = c.SetBytes(ctx, "k", []byte("v"), 0)
			m := mem(c)
			if _, ok := m.items[tc.want]; !ok || len(m.items) != 1 {
				t.Fatalf("stored keys = %v, want %q", keys(m), tc.want)
			}
		})
	}
}

func keys(m *memory) []string {
	var out []string
	for k := range m.items {
		out = append(out, k)
	}
	return out
}

func TestEscapeComponent(t *testing.T) {
	cases := map[string]string{
		tenantA:  tenantA,
		"my-app": "my-app",
		"a:b":    "a%3Ab",
		"a%3Ab":  "a%253Ab",
		"x\ny":   "x%0Ay",
		"%":      "%25",
	}
	for in, want := range cases {
		if got := escapeComponent(in); got != want {
			t.Errorf("escape(%q) = %q, want %q", in, got, want)
		}
	}
	// identities that would collide unescaped stay distinct
	a, _ := newKeyspace(Config{Limits: LimitsConfig{MaxKeyBytes: 1024}}, options{env: "e", app: "x:y", hasNamespace: true})
	b, _ := newKeyspace(Config{Limits: LimitsConfig{MaxKeyBytes: 1024}}, options{env: "e:x", app: "y", hasNamespace: true})
	ka, _ := a.resolve(bg, "k")
	kb, _ := b.resolve(bg, "k")
	if ka == kb {
		t.Fatalf("namespaces collide: %q", ka)
	}
}

func TestTenantIsolation(t *testing.T) {
	c, _ := openMemory(t, nil, WithTenantResolver(tenantFrom))
	f := wrapFaults(c)
	ctxA, ctxB := withTenant(bg, tenantA), withTenant(bg, tenantB)

	_ = c.SetBytes(ctxA, "same", []byte("A"), 0)
	_ = c.SetBytes(ctxB, "same", []byte("B"), 0)
	if got, _ := c.GetBytes(ctxA, "same"); string(got) != "A" {
		t.Fatalf("tenant A sees %q", got)
	}
	if got, _ := c.GetBytes(ctxB, "same"); string(got) != "B" {
		t.Fatalf("tenant B sees %q", got)
	}
	if n, _ := c.Delete(ctxA, "same"); n != 1 {
		t.Fatal("delete")
	}
	if ok, _ := c.Exists(ctxB, "same"); !ok {
		t.Fatal("tenant A deleted tenant B's key")
	}

	// a missing tenant fails before any backend access or loader call
	before := f.anyCall.Load()
	loaded := false
	ops := map[string]error{
		"get":       c.Get(bg, "k", new(User)),
		"get_bytes": func() error { _, err := c.GetBytes(bg, "k"); return err }(),
		"set":       c.Set(bg, "k", User{Name: "x"}, 0),
		"set_bytes": c.SetBytes(bg, "k", nil, 0),
		"nx":        func() error { _, err := c.SetBytesIfAbsent(bg, "k", nil, 0); return err }(),
		"delete":    func() error { _, err := c.Delete(bg, "k"); return err }(),
		"exists":    func() error { _, err := c.Exists(bg, "k"); return err }(),
		"expire":    func() error { _, err := c.Expire(bg, "k", time.Second); return err }(),
		"ttl":       func() error { _, err := c.TTL(bg, "k"); return err }(),
		"incr":      func() error { _, err := c.Increment(bg, "k", 1); return err }(),
		"load": c.GetOrLoad(bg, "k", new(User), 0, func(context.Context) (any, error) {
			loaded = true
			return User{Name: "x"}, nil
		}),
	}
	for name, err := range ops {
		if !errors.Is(err, ErrTenantRequired) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if f.anyCall.Load() != before || loaded {
		t.Fatal("a missing tenant reached the backend or the loader")
	}
	// Ping needs no tenant
	if err := c.Ping(bg); err != nil {
		t.Fatal(err)
	}
}

func TestErrorsDoNotLeakKeysOrTenants(t *testing.T) {
	c, _ := openMemory(t, nil, WithTenantResolver(tenantFrom))
	ctx := withTenant(bg, tenantA)
	_ = c.SetBytes(ctx, "secret-key", []byte("not-a-number"), 0)
	_, err := c.Increment(ctx, "secret-key", 1)
	msg := err.Error()
	if strings.Contains(msg, "secret-key") || strings.Contains(msg, tenantA) || strings.Contains(msg, "not-a-number") {
		t.Fatalf("error leaks data: %s", msg)
	}
	var ce *Error
	if !errors.As(err, &ce) || ce.Op != "increment" || ce.Driver != DriverMemory || ce.Kind != ErrNotInteger {
		t.Fatalf("typed error = %+v", ce)
	}
}

func TestContextAndLifecycle(t *testing.T) {
	c, _ := openMemory(t, nil)
	ctx, cancel := context.WithCancel(bg)
	cancel()
	mustIs(t, c.SetBytes(ctx, "k", nil, 0), context.Canceled)
	dctx, dcancel := context.WithDeadline(bg, time.Now().Add(-time.Second))
	defer dcancel()
	_, err := c.GetBytes(dctx, "k")
	mustIs(t, err, context.DeadlineExceeded)

	if err := c.Shutdown(bg); err != nil {
		t.Fatal(err)
	}
	if err := c.Shutdown(bg); err != nil {
		t.Fatal("second Shutdown must be a no-op:", err)
	}
	mustIs(t, c.Ping(bg), ErrClosed)
	mustIs(t, c.SetBytes(bg, "k", nil, 0), ErrClosed)
	if _, err := c.Delete(bg); !errors.Is(err, ErrClosed) {
		t.Fatal("Delete without keys must still honor the lifecycle:", err)
	}
}

func TestShutdownWaitsForActiveWork(t *testing.T) {
	c, _ := openMemory(t, nil)
	started, release := make(chan struct{}), make(chan struct{})
	go func() {
		_ = c.GetOrLoad(bg, "k", new(User), 0, func(ctx context.Context) (any, error) {
			close(started)
			<-release // ignores ctx on purpose: an uncooperative loader
			return User{Name: "x"}, nil
		})
	}()
	<-started

	short, cancel := context.WithTimeout(bg, 20*time.Millisecond)
	defer cancel()
	mustIs(t, c.Shutdown(short), context.DeadlineExceeded)
	mustIs(t, c.SetBytes(bg, "other", nil, 0), ErrClosed) // admission stays closed

	close(release)
	if err := c.Shutdown(bg); err != nil { // a later call waits for the remaining work
		t.Fatal(err)
	}
	if len(mem(c).items) != 0 {
		t.Fatal("memory must be cleared after shutdown")
	}
}

func TestConcurrentOperationsAndShutdown(t *testing.T) {
	c, _ := openMemory(t, func(cfg *Config) { cfg.Memory.MaxEntries = 50 })
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := range 8 {
		wg.Go(func() {
			for j := 0; ; j++ {
				select {
				case <-stop:
					return
				default:
				}
				k := string(rune('a' + (i+j)%20))
				_ = c.Set(bg, k, User{Name: k}, time.Duration(j%3)*time.Millisecond)
				_ = c.Get(bg, k, new(User))
				_, _ = c.SetBytesIfAbsent(bg, k+"nx", []byte("1"), 0)
				_, _ = c.Increment(bg, "n", 1)
				_, _ = c.Delete(bg, k)
				_ = c.GetOrLoad(bg, "load"+k, new(User), time.Millisecond, func(context.Context) (any, error) {
					return User{Name: "l"}, nil
				})
				mem(c).cleanupOnce()
			}
		})
	}
	time.Sleep(20 * time.Millisecond)
	if err := c.Shutdown(bg); err != nil {
		t.Fatal(err)
	}
	close(stop)
	wg.Wait()
}
