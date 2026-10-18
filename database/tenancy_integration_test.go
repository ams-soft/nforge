//go:build integration

package database

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"

	sq "github.com/Masterminds/squirrel"
	"github.com/jackc/pgx/v5"
)

const (
	tenantA = "11111111-1111-4111-8111-111111111111"
	tenantB = "22222222-2222-4222-a222-222222222222"

	rlsRole     = "nforge_rls_app"
	rlsPassword = "nforge_rls_app"
)

// rlsFixture creates, as the (superuser) test role, a non-privileged role and
// two tables: nforge_tenant_items with a forced tenant policy, and
// nforge_tenant_nopolicy with RLS enabled but no policy. It returns the DSN
// of the restricted role.
func rlsFixture(t *testing.T) (adminDSN, appDSN string) {
	t.Helper()
	adminDSN = os.Getenv("NFORGE_TEST_DATABASE_URL")
	if adminDSN == "" {
		t.Skip("NFORGE_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)

	cleanup := `
		DROP TABLE IF EXISTS nforge_tenant_items, nforge_tenant_nopolicy;
		DO $$ BEGIN
			IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '` + rlsRole + `') THEN
				EXECUTE 'DROP OWNED BY ` + rlsRole + `';
				EXECUTE 'DROP ROLE ` + rlsRole + `';
			END IF;
		END $$;`
	if _, err := conn.Exec(ctx, cleanup); err != nil {
		t.Fatal(err)
	}
	setup := `
		CREATE ROLE ` + rlsRole + ` LOGIN PASSWORD '` + rlsPassword + `';
		CREATE TABLE nforge_tenant_items (
			id BIGSERIAL PRIMARY KEY,
			tenant_id uuid NOT NULL DEFAULT NULLIF(current_setting('app.tenant_id', true), '')::uuid,
			name TEXT NOT NULL
		);
		ALTER TABLE nforge_tenant_items ENABLE ROW LEVEL SECURITY;
		ALTER TABLE nforge_tenant_items FORCE ROW LEVEL SECURITY;
		CREATE POLICY tenant_isolation ON nforge_tenant_items
			USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
			WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);
		CREATE TABLE nforge_tenant_nopolicy (id INT, tenant_id uuid);
		ALTER TABLE nforge_tenant_nopolicy ENABLE ROW LEVEL SECURITY;
		GRANT USAGE ON SCHEMA public TO ` + rlsRole + `;
		GRANT SELECT, INSERT, UPDATE, DELETE ON nforge_tenant_items, nforge_tenant_nopolicy TO ` + rlsRole + `;
		GRANT USAGE ON SEQUENCE nforge_tenant_items_id_seq TO ` + rlsRole + `;`
	if _, err := conn.Exec(ctx, setup); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, err := pgx.Connect(context.Background(), adminDSN)
		if err != nil {
			return
		}
		defer c.Close(context.Background())
		_, _ = c.Exec(context.Background(), cleanup)
	})

	u, err := url.Parse(adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword(rlsRole, rlsPassword)
	return adminDSN, u.String()
}

func rlsTenancy(tables ...string) TenancyConfig {
	return TenancyConfig{Enabled: true, Mode: TenancyRLS, Tables: tables}
}

func openRLS(t *testing.T, dsn string, maxConns int32) *DB {
	t.Helper()
	ctx := context.Background()
	db, err := Open(ctx, Config{DSN: dsn, Pool: PoolConfig{MaxConns: maxConns}, Tenancy: rlsTenancy("nforge_tenant_items")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Shutdown(ctx) })
	return db
}

func mustTenant(t *testing.T, db *DB, id string) *DB {
	t.Helper()
	scoped, err := db.ForTenant(id)
	if err != nil {
		t.Fatal(err)
	}
	return scoped
}

func names(t *testing.T, ctx context.Context, e DBTX) []string {
	t.Helper()
	rows, err := e.Query(ctx, "SELECT name FROM nforge_tenant_items ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestIntegrationTenancyCheckRejectsBypassRoleAndBadTables(t *testing.T) {
	adminDSN, appDSN := rlsFixture(t)
	ctx := context.Background()

	_, err := Open(ctx, Config{DSN: adminDSN, Tenancy: rlsTenancy()})
	if !errors.Is(err, ErrTenancyCheck) || !strings.Contains(err.Error(), "BYPASSRLS") {
		t.Fatalf("superuser: err = %v", err)
	}

	_, err = Open(ctx, Config{DSN: appDSN, Tenancy: rlsTenancy("nforge_tenant_nopolicy", "public.missing", "nforge_tenant_items")})
	if !errors.Is(err, ErrTenancyCheck) {
		t.Fatalf("bad tables: err = %v", err)
	}
	for _, want := range []string{"nforge_tenant_nopolicy: no policy", "public.missing: table not found"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to mention %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "nforge_tenant_items") {
		t.Errorf("valid table reported: %v", err)
	}

	col := Config{DSN: appDSN, Tenancy: TenancyConfig{Enabled: true, Mode: TenancyColumn, Column: "owner_id", Tables: []string{"nforge_tenant_items"}}}
	if _, err := Open(ctx, col); !errors.Is(err, ErrTenancyCheck) || !strings.Contains(err.Error(), "column owner_id not found") {
		t.Fatalf("column mode: err = %v", err)
	}
}

func TestIntegrationRLSIsolatesTenants(t *testing.T) {
	_, appDSN := rlsFixture(t)
	ctx := context.Background()
	// one connection: every call below reuses it, so a leaked setting would show up
	db := openRLS(t, appDSN, 1)
	a, b := mustTenant(t, db, tenantA), mustTenant(t, db, tenantB)

	// Exec relies on the column DEFAULT reading the setting
	if _, err := a.Executor.Exec(ctx, "INSERT INTO nforge_tenant_items (name) VALUES ($1)", "a1"); err != nil {
		t.Fatal(err)
	}
	// Squirrel with explicit tenant column
	query, args, err := b.Query.Insert("nforge_tenant_items").Columns(b.TenantColumn(), "name").Values(b.TenantID(), "b1").ToSql()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Executor.Exec(ctx, query, args...); err != nil {
		t.Fatal(err)
	}

	if got := names(t, ctx, a.Executor); len(got) != 1 || got[0] != "a1" {
		t.Errorf("tenant A sees %v", got)
	}
	if got := names(t, ctx, b.Executor); len(got) != 1 || got[0] != "b1" {
		t.Errorf("tenant B sees %v", got)
	}
	if got := names(t, ctx, db.Executor); len(got) != 0 {
		t.Errorf("unscoped pool sees %v, want nothing", got)
	}

	var setting string
	if err := db.Executor.QueryRow(ctx, "SELECT coalesce(current_setting('app.tenant_id', true), '')").Scan(&setting); err != nil {
		t.Fatal(err)
	}
	if setting != "" {
		t.Errorf("setting leaked to the pooled connection: %q", setting)
	}

	// WITH CHECK: tenant A cannot write a row of tenant B
	_, err = a.Executor.Exec(ctx, "INSERT INTO nforge_tenant_items (tenant_id, name) VALUES ($1, 'x')", tenantB)
	if err == nil || !strings.Contains(err.Error(), "row-level security") {
		t.Errorf("cross-tenant insert: err = %v", err)
	}

	// QueryRow, including ErrNoRows
	var n int
	if err := a.Executor.QueryRow(ctx, "SELECT count(*) FROM nforge_tenant_items").Scan(&n); err != nil || n != 1 {
		t.Errorf("count = %d, %v", n, err)
	}
	var name string
	if err := a.Executor.QueryRow(ctx, "SELECT name FROM nforge_tenant_items WHERE name = 'b1'").Scan(&name); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("other tenant row: err = %v", err)
	}

	// SendBatch: the caller's queries keep their own results
	batch := &pgx.Batch{}
	batch.Queue("INSERT INTO nforge_tenant_items (name) VALUES ('a2')")
	batch.Queue("SELECT count(*) FROM nforge_tenant_items")
	br := a.Executor.SendBatch(ctx, batch)
	if _, err := br.Exec(); err != nil {
		t.Fatal(err)
	}
	if err := br.QueryRow().Scan(&n); err != nil || n != 2 {
		t.Errorf("batch count = %d, %v", n, err)
	}
	if err := br.Close(); err != nil {
		t.Fatal(err)
	}

	// PostgreSQL rejects COPY FROM on tables with row level security
	_, err = b.Executor.CopyFrom(ctx, pgx.Identifier{"nforge_tenant_items"}, []string{"name"},
		pgx.CopyFromRows([][]any{{"b2"}}))
	if err == nil || !strings.Contains(err.Error(), "row-level security") {
		t.Errorf("copy into RLS table: err = %v", err)
	}
	if got := names(t, ctx, b.Executor); strings.Join(got, ",") != "b1" {
		t.Errorf("tenant B after failed copy sees %v", got)
	}
}

func TestIntegrationRLSTransaction(t *testing.T) {
	_, appDSN := rlsFixture(t)
	ctx := context.Background()
	db := openRLS(t, appDSN, 2)
	a := mustTenant(t, db, tenantA)

	err := a.Transaction(ctx, func(tx *DB) error {
		if tx.TenantID() != tenantA {
			t.Errorf("tx tenant = %q", tx.TenantID())
		}
		if _, err := tx.Executor.Exec(ctx, "INSERT INTO nforge_tenant_items (name) VALUES ('t1'), ('t2')"); err != nil {
			return err
		}
		query, args, err := tx.Query.Select("name").From("nforge_tenant_items").
			Where(tx.TenantEq()).Where(sq.Eq{"name": "t2"}).ToSql()
		if err != nil {
			return err
		}
		var name string
		return tx.Executor.QueryRow(ctx, query, args...).Scan(&name)
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := names(t, ctx, a.Executor); strings.Join(got, ",") != "t1,t2" {
		t.Errorf("after commit tenant A sees %v", got)
	}

	sentinel := errors.New("rollback")
	err = a.Transaction(ctx, func(tx *DB) error {
		_, _ = tx.Executor.Exec(ctx, "INSERT INTO nforge_tenant_items (name) VALUES ('t3')")
		return sentinel
	})
	if !errors.Is(err, sentinel) || len(names(t, ctx, a.Executor)) != 2 {
		t.Errorf("rollback: err = %v", err)
	}

	tenantCtx, err := WithTenant(ctx, strings.ToUpper(tenantB))
	if err != nil {
		t.Fatal(err)
	}
	b, err := db.ForContext(tenantCtx)
	if err != nil || b.TenantID() != tenantB {
		t.Fatalf("ForContext = %v, %v", b, err)
	}
	if got := names(t, ctx, b.Executor); len(got) != 0 {
		t.Errorf("tenant B sees %v", got)
	}
}
