package database

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/ams-soft/nforge/database/internal/rlsexec"

	sq "github.com/Masterminds/squirrel"
)

const testTenant = "3f2b8c1e-9d4a-4e6b-8a7c-1b2c3d4e5f60"

func TestParseTenantID(t *testing.T) {
	tests := []struct {
		in, want string
		ok       bool
	}{
		{testTenant, testTenant, true},
		{"3F2B8C1E-9D4A-4E6B-BA7C-1B2C3D4E5F60", "3f2b8c1e-9d4a-4e6b-ba7c-1b2c3d4e5f60", true},
		{"", "", false},
		{"3f2b8c1e9d4a4e6b8a7c1b2c3d4e5f60", "", false},                // no hyphens
		{"3f2b8c1e-9d4a-1e6b-8a7c-1b2c3d4e5f60", "", false},            // version 1
		{"3f2b8c1e-9d4a-4e6b-ca7c-1b2c3d4e5f60", "", false},            // wrong variant
		{"3f2b8c1e-9d4a-4e6b-8a7c-1b2c3d4e5f6g", "", false},            // not hex
		{"3f2b8c1e-9d4a-4e6b-8a7c_1b2c3d4e5f60", "", false},            // separator
		{"{3f2b8c1e-9d4a-4e6b-8a7c-1b2c3d4e5f60}", "", false},          // braces
		{"00000000-0000-0000-0000-000000000000", "", false},            // nil uuid
		{"3f2b8c1e-9d4a-4e6b-8a7c-1b2c3d4e5f60' OR '1'='1", "", false}, // injection attempt
	}
	for _, tt := range tests {
		got, err := ParseTenantID(tt.in)
		if tt.ok != (err == nil) || got != tt.want {
			t.Errorf("ParseTenantID(%q) = %q, %v", tt.in, got, err)
		}
		if err != nil && !errors.Is(err, ErrInvalidTenantID) {
			t.Errorf("ParseTenantID(%q) error = %v, want ErrInvalidTenantID", tt.in, err)
		}
	}
}

func TestTenancyConfigValidate(t *testing.T) {
	valid := TenancyConfig{Enabled: true, Mode: "RLS", Tables: []string{"users", "billing.invoices"}}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid config: %v", err)
	}
	if err := (TenancyConfig{Mode: "bogus", Column: "x;"}).Validate(); err != nil {
		t.Fatalf("disabled config must be valid: %v", err)
	}

	bad := map[string]TenancyConfig{
		"missing mode":   {Enabled: true},
		"unknown mode":   {Enabled: true, Mode: "schema"},
		"column":         {Enabled: true, Mode: "column", Column: "tenant_id; DROP TABLE x"},
		"schema":         {Enabled: true, Mode: "column", Schema: "a-b"},
		"setting no dot": {Enabled: true, Mode: "rls", Setting: "tenant_id"},
		"setting quote":  {Enabled: true, Mode: "rls", Setting: "app.tenant'"},
		"table":          {Enabled: true, Mode: "rls", Tables: []string{"a.b.c"}},
		"empty table":    {Enabled: true, Mode: "rls", Tables: []string{""}},
	}
	for name, cfg := range bad {
		if err := cfg.Validate(); !errors.Is(err, ErrInvalidTenancy) {
			t.Errorf("%s: err = %v, want ErrInvalidTenancy", name, err)
		}
	}
	// a column-mode setting is ignored, so it need not be valid
	if err := (TenancyConfig{Enabled: true, Mode: "column", Setting: "nodot"}).Validate(); err != nil {
		t.Errorf("column mode with unused setting: %v", err)
	}
}

func TestTenancyDefaults(t *testing.T) {
	tables := []string{"users"}
	db := New(lazyPool(t), WithTenancy(TenancyConfig{Enabled: true, Mode: " Column ", Tables: tables}))
	got := db.Tenancy()
	want := TenancyConfig{Enabled: true, Mode: TenancyColumn, Column: "tenant_id", Schema: "public", Setting: "app.tenant_id", Tables: []string{"users"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Tenancy() = %+v, want %+v", got, want)
	}
	got.Tables[0] = "changed"
	tables[0] = "changed"
	if db.Tenancy().Tables[0] != "users" {
		t.Error("Tenancy() shares its Tables slice")
	}
	if New(lazyPool(t)).Tenancy().Enabled {
		t.Error("tenancy must be disabled by default")
	}
}

func TestNewPanicsOnInvalidTenancy(t *testing.T) {
	expectPanic(t, ErrInvalidTenancy, func() {
		New(lazyPool(t), WithTenancy(TenancyConfig{Enabled: true, Mode: "nope"}))
	})
}

func TestOpenInvalidTenancyDoesNotDial(t *testing.T) {
	cfg := Config{DSN: "postgres://u:p@127.0.0.1:1/db", Tenancy: TenancyConfig{Enabled: true}}
	db, err := Open(context.Background(), cfg)
	if db != nil || !errors.Is(err, ErrInvalidTenancy) {
		t.Fatalf("got %v, %v", db, err)
	}
}

func TestTenantContext(t *testing.T) {
	ctx := context.Background()
	if _, ok := TenantFromContext(ctx); ok {
		t.Fatal("empty context has a tenant")
	}
	if got, err := WithTenant(ctx, "not-a-uuid"); !errors.Is(err, ErrInvalidTenantID) || got != ctx {
		t.Fatalf("invalid id: %v", err)
	}
	ctx, err := WithTenant(ctx, "3F2B8C1E-9D4A-4E6B-8A7C-1B2C3D4E5F60")
	if err != nil {
		t.Fatal(err)
	}
	if id, ok := TenantFromContext(ctx); !ok || id != testTenant {
		t.Fatalf("TenantFromContext = %q, %v", id, ok)
	}
}

func TestForTenant(t *testing.T) {
	pool := lazyPool(t)

	if _, err := New(pool).ForTenant(testTenant); !errors.Is(err, ErrTenancyDisabled) {
		t.Errorf("disabled: err = %v", err)
	}
	if _, err := New(pool).ForContext(context.Background()); !errors.Is(err, ErrTenancyDisabled) {
		t.Errorf("disabled ForContext: err = %v", err)
	}

	rls := New(pool, WithTenancy(TenancyConfig{Enabled: true, Mode: TenancyRLS, Setting: "app.org"}))
	if _, err := rls.ForTenant("x"); !errors.Is(err, ErrInvalidTenantID) {
		t.Errorf("invalid id: err = %v", err)
	}
	if _, err := rls.ForContext(context.Background()); !errors.Is(err, ErrNoTenant) {
		t.Errorf("no tenant in ctx: err = %v", err)
	}
	if _, err := (&DB{pool: pool, tenancy: rls.tenancy, transaction: true}).ForTenant(testTenant); !errors.Is(err, ErrTenantInTransaction) {
		t.Errorf("in transaction: err = %v", err)
	}

	scoped, err := rls.ForTenant(testTenant)
	if err != nil {
		t.Fatal(err)
	}
	e, ok := scoped.Executor.(*rlsexec.Executor)
	if !ok || e.TenantID != testTenant || e.Setting != "app.org" || e.Pool != pool {
		t.Fatalf("rls executor = %#v", scoped.Executor)
	}
	if scoped.TenantID() != testTenant || rls.TenantID() != "" {
		t.Error("tenant must be set only on the scoped DB")
	}
	if err := scoped.Shutdown(context.Background()); !errors.Is(err, ErrShutdownTenantScoped) {
		t.Errorf("Shutdown on scoped DB: err = %v", err)
	}

	col := New(pool, WithTenancy(TenancyConfig{Enabled: true, Mode: TenancyColumn}))
	scoped, err = col.ForTenant(testTenant)
	if err != nil {
		t.Fatal(err)
	}
	if scoped.Executor != DBTX(pool) {
		t.Error("column mode must run on the pool")
	}
}

func TestTenantEq(t *testing.T) {
	pool := lazyPool(t)
	db := New(pool, WithTenancy(TenancyConfig{Enabled: true, Mode: TenancyColumn, Column: "org_id"}))
	scoped, err := db.ForTenant(testTenant)
	if err != nil {
		t.Fatal(err)
	}

	query, args, err := scoped.Query.Select("u.id").From("users u").
		Where(scoped.TenantEqOn("u")).
		Where(sq.Or{sq.Eq{"u.name": "a"}, sq.Eq{"u.name": "b"}}).ToSql()
	if err != nil {
		t.Fatal(err)
	}
	if want := "SELECT u.id FROM users u WHERE u.org_id = $1 AND (u.name = $2 OR u.name = $3)"; query != want {
		t.Errorf("query = %q, want %q", query, want)
	}
	if !reflect.DeepEqual(args, []any{testTenant, "a", "b"}) {
		t.Errorf("args = %v", args)
	}

	if _, _, err := db.Query.Select("id").From("users").Where(db.TenantEq()).ToSql(); !errors.Is(err, ErrNoTenant) {
		t.Errorf("unscoped TenantEq: err = %v", err)
	}
	if _, _, err := New(pool).TenantEq().ToSql(); !errors.Is(err, ErrTenancyDisabled) {
		t.Errorf("disabled TenantEq: err = %v", err)
	}
	if _, _, err := scoped.TenantEqOn("u; --").ToSql(); err == nil {
		t.Error("invalid qualifier accepted")
	}
	if scoped.TenantColumn() != "org_id" || New(pool).TenantColumn() != "" {
		t.Error("TenantColumn")
	}
}
