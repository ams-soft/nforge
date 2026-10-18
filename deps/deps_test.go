package deps

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ams-soft/nforge/cache"
	"github.com/ams-soft/nforge/config"
	"github.com/ams-soft/nforge/database"
	"github.com/ams-soft/nforge/jose"
)

func TestShutdownRunsLIFOAndJoinsErrors(t *testing.T) {
	var order []string
	errDB := errors.New("db")
	d := Deps{Aux: &Aux{}}
	d.addShutdownFunc(func(context.Context) error { order = append(order, "logger"); return nil })
	d.addShutdownFunc(func(context.Context) error { order = append(order, "tracing"); return nil })
	d.addShutdownFunc(func(context.Context) error { order = append(order, "database"); return errDB })

	err := d.Shutdown(context.Background())
	if !errors.Is(err, errDB) {
		t.Fatalf("err = %v", err)
	}
	if want := []string{"database", "tracing", "logger"}; !slices.Equal(order, want) {
		t.Fatalf("order = %v, want %v", order, want)
	}

	order = nil
	if err := d.Shutdown(context.Background()); err != nil || order != nil {
		t.Fatalf("second Shutdown must be a no-op: %v %v", err, order)
	}
}

func TestPingDatabaseDisabled(t *testing.T) {
	var d Deps
	if err := d.PingDatabase(context.Background()); !errors.Is(err, ErrDatabaseDisabled) {
		t.Fatalf("err = %v", err)
	}
}

func TestCheckJOSEDisabled(t *testing.T) {
	var d Deps
	if err := d.CheckJOSE(context.Background()); !errors.Is(err, ErrJOSEDisabled) {
		t.Fatalf("err = %v", err)
	}
}

func TestSetupJOSEDisabledLeavesNil(t *testing.T) {
	d := Deps{Context: context.Background(), Aux: &Aux{}}
	d.setupJOSE(config.Config{})
	if d.JOSE != nil || len(d.Aux.ShutdownFuncs) != 0 {
		t.Fatal("disabled jose must not create a service or a shutdown func")
	}
}

func TestSetupJOSEEnabledRegistersShutdown(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	der, _ := x509.MarshalPKCS8PrivateKey(priv)
	path := filepath.Join(t.TempDir(), "k.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	var cfg config.Config
	cfg.JOSE = jose.Config{
		Enabled: true,
		Keys: []jose.KeyConfig{{KID: "k", Algorithm: jose.EdDSA, Operations: []string{jose.OpSign, jose.OpVerify},
			Source: jose.KeySourceConfig{Format: "pem", File: path}}},
		Profiles: []jose.ProfileConfig{{Name: "msg", Kind: jose.KindJWS, Typ: "msg+jws", SigningAlgorithm: jose.EdDSA, SigningKey: "k"}},
	}
	d := Deps{Context: context.Background(), Logger: *slog.New(slog.DiscardHandler), Aux: &Aux{}}
	d.setupJOSE(cfg)
	if d.JOSE == nil || len(d.Aux.ShutdownFuncs) != 1 {
		t.Fatal("enabled jose must create the service and register its shutdown")
	}
	if err := d.CheckJOSE(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := d.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := d.CheckJOSE(context.Background()); !errors.Is(err, jose.ErrClosed) {
		t.Fatalf("after shutdown: %v", err)
	}
}

func TestPingCacheDisabled(t *testing.T) {
	var d Deps
	if err := d.PingCache(context.Background()); !errors.Is(err, ErrCacheDisabled) {
		t.Fatalf("err = %v", err)
	}
}

func TestSetupCacheDisabledLeavesNil(t *testing.T) {
	d := Deps{Context: context.Background(), Aux: &Aux{}}
	d.setupCache(config.Config{})
	if d.Cache != nil || len(d.Aux.ShutdownFuncs) != 0 {
		t.Fatal("disabled cache must not create a cache or a shutdown func")
	}
}

func newCacheDeps(t *testing.T, tenancy bool) Deps {
	t.Helper()
	var cfg config.Config
	cfg.Env, cfg.App.Name = "local", "my-app"
	cfg.Cache = cache.Config{Enabled: true, Driver: cache.DriverMemory}
	cfg.Database.Tenancy.Enabled = tenancy
	d := Deps{Context: context.Background(), Logger: *slog.New(slog.DiscardHandler), Aux: &Aux{}}
	d.setupCodec(cfg)
	d.setupCache(cfg)
	if d.Cache == nil || len(d.Aux.ShutdownFuncs) != 1 {
		t.Fatal("enabled cache must be created with its shutdown registered")
	}
	return d
}

func TestSetupCacheRegistersShutdown(t *testing.T) {
	d := newCacheDeps(t, false)
	ctx := context.Background()
	if err := d.PingCache(ctx); err != nil {
		t.Fatal(err)
	}
	if err := d.Cache.SetBytes(ctx, "k", []byte("v"), 0); err != nil {
		t.Fatal("single-tenant cache must not need a tenant:", err)
	}
	if err := d.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := d.PingCache(ctx); !errors.Is(err, cache.ErrClosed) {
		t.Fatalf("after shutdown: %v", err)
	}
}

func TestSetupCacheUsesFrameworkTenant(t *testing.T) {
	d := newCacheDeps(t, true)
	t.Cleanup(func() { _ = d.Shutdown(context.Background()) })
	ctx := context.Background()
	if err := d.Cache.SetBytes(ctx, "k", []byte("v"), 0); !errors.Is(err, cache.ErrTenantRequired) {
		t.Fatalf("without tenant: %v", err)
	}
	if err := d.PingCache(ctx); err != nil {
		t.Fatal("ping needs no tenant:", err)
	}
	tctx, err := database.WithTenant(ctx, "11111111-1111-4111-8111-111111111111")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Cache.SetBytes(tctx, "k", []byte("v"), 0); err != nil {
		t.Fatal(err)
	}
	other, _ := database.WithTenant(ctx, "22222222-2222-4222-8222-222222222222")
	if _, err := d.Cache.GetBytes(other, "k"); !errors.Is(err, cache.ErrNotFound) {
		t.Fatalf("other tenant must not see the key: %v", err)
	}
}

func TestErrorPrefix(t *testing.T) {
	for _, err := range []error{ErrDatabaseDisabled, ErrJOSEDisabled, ErrCacheDisabled} {
		if !strings.HasPrefix(err.Error(), "deps: ") {
			t.Errorf("%q lacks the package prefix", err)
		}
	}
}
