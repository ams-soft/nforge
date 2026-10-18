package database

import (
	"context"
	"errors"
	"strings"
	"testing"

	sq "github.com/Masterminds/squirrel"
	"github.com/jackc/pgx/v5/pgxpool"
)

func lazyPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	// pgxpool is lazy: nothing is dialed until a connection is acquired.
	pool, err := pgxpool.New(context.Background(), "postgres://u:p@127.0.0.1:1/db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func expectPanic(t *testing.T, want error, fn func()) {
	t.Helper()
	defer func() {
		r := recover()
		if err, ok := r.(error); !ok || !errors.Is(err, want) {
			t.Errorf("panic = %v, want %v", r, want)
		}
	}()
	fn()
}

func TestNewPanicsOnNilPool(t *testing.T) {
	expectPanic(t, ErrNilPool, func() { New(nil) })
}

func TestOpenInvalidConfigDoesNotDial(t *testing.T) {
	db, err := Open(context.Background(), Config{Driver: "mysql"})
	if db != nil || !errors.Is(err, ErrUnsupportedDriver) {
		t.Fatalf("got %v, %v", db, err)
	}
}

func TestNewRootDB(t *testing.T) {
	pool := lazyPool(t)
	db := New(pool)

	if db.Executor != DBTX(pool) {
		t.Error("root DB executor must be the pool")
	}
	if db.transaction || db.pool != pool {
		t.Error("root DB must be non-transactional and own the pool")
	}
}

func TestQueryBuilderUsesDollarPlaceholders(t *testing.T) {
	db := New(lazyPool(t))

	query, args, err := db.Query.Select("id").From("t").Where(sq.Eq{"a": 1}).ToSql()
	if err != nil || query != "SELECT id FROM t WHERE a = $1" || len(args) != 1 || args[0] != 1 {
		t.Fatalf("got %q %v %v", query, args, err)
	}

	sub := db.Query.Select("id").From("u").Where(sq.Eq{"b": 2})

	query, _, err = db.Query.Select("*").FromSelect(sub, "s").Where(sq.Eq{"a": 1}).ToSql()
	want := "SELECT * FROM (SELECT id FROM u WHERE b = $1) AS s WHERE a = $2"
	if err != nil || query != want {
		t.Errorf("FromSelect: got %q, %v; want %q", query, err, want)
	}

	query, _, err = db.Query.Select("*").From("t").
		Where(sq.Eq{"a": 1}).
		Where(sub.Prefix("id IN (").Suffix(")")).
		ToSql()
	want = "SELECT * FROM t WHERE a = $1 AND id IN ( SELECT id FROM u WHERE b = $2 )"
	if err != nil || query != want {
		t.Errorf("Where(builder): got %q, %v; want %q", query, err, want)
	}

	// Pitfall: a $-configured builder nested through sq.Expr is NOT renumbered
	// (both placeholders become $1). Nest via FromSelect / Where(builder), or
	// give the sub-builder PlaceholderFormat(sq.Question).
	query, _, _ = db.Query.Select("*").From("t").
		Where(sq.Eq{"a": 1}).
		Where(sq.Expr("id IN (?)", sub.PlaceholderFormat(sq.Question))).
		ToSql()
	want = "SELECT * FROM t WHERE a = $1 AND id IN (SELECT id FROM u WHERE b = $2)"
	if query != want {
		t.Errorf("Expr(question sub): got %q; want %q", query, want)
	}
}

func TestTransactionScopedGuards(t *testing.T) {
	pool := lazyPool(t)
	txDB := &DB{pool: pool, transaction: true}

	if err := txDB.Transaction(context.Background(), func(*DB) error { return nil }); !errors.Is(err, ErrNestedTransaction) {
		t.Errorf("nested: got %v", err)
	}
	if err := txDB.Shutdown(context.Background()); !errors.Is(err, ErrShutdownInTransaction) {
		t.Errorf("shutdown: got %v", err)
	}
}

func TestShutdownIsIdempotent(t *testing.T) {
	db := New(lazyPool(t))
	if err := db.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := db.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestErrorPrefix(t *testing.T) {
	for _, err := range []error{ErrNestedTransaction, ErrShutdownInTransaction, ErrShutdownTenantScoped,
		ErrTenancyDisabled, ErrNoTenant, ErrInvalidTenantID, ErrTenantInTransaction, ErrInvalidTenancy,
		ErrTenancyCheck, ErrNilPool, ErrUnsupportedDriver, ErrMissingConfig} {
		if !strings.HasPrefix(err.Error(), "database: ") {
			t.Errorf("%q lacks the package prefix", err)
		}
	}
}
