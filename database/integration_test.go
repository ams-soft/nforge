//go:build integration

// Integration tests against a real PostgreSQL. Run with:
//
//	NFORGE_TEST_DATABASE_URL=postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable \
//	  go test -tags integration ./database/
package database

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/jackc/pgx/v5"
)

// Helpers that accept DBTX work both on the pool and inside a transaction.
func txID(ctx context.Context, e DBTX) (int64, error) {
	var id int64
	err := e.QueryRow(ctx, "SELECT txid_current()").Scan(&id)
	return id, err
}

func insertItem(ctx context.Context, e DBTX, name string) error {
	_, err := e.Exec(ctx, "INSERT INTO nforge_items (name) VALUES ($1)", name)
	return err
}

func openTestDB(t *testing.T) *DB {
	t.Helper()
	dsn := os.Getenv("NFORGE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("NFORGE_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	db, err := Open(ctx, Config{DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Shutdown(ctx) })

	if _, err := db.Executor.Exec(ctx, `
		DROP TABLE IF EXISTS nforge_items;
		CREATE TABLE nforge_items (id BIGSERIAL PRIMARY KEY, name TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'pending')`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Executor.Exec(ctx, "DROP TABLE IF EXISTS nforge_items") })
	return db
}

func countItems(t *testing.T, db *DB) int {
	t.Helper()
	var n int
	if err := db.Executor.QueryRow(context.Background(), "SELECT count(*) FROM nforge_items").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// writeBoth writes one row with raw SQL and one through tx.Query + tx.Executor.
func writeBoth(ctx context.Context, tx *DB) error {
	if err := insertItem(ctx, tx.Executor, "via-raw"); err != nil {
		return err
	}
	query, args, err := tx.Query.Insert("nforge_items").Columns("name").Values("via-squirrel").ToSql()
	if err != nil {
		return err
	}
	_, err = tx.Executor.Exec(ctx, query, args...)
	return err
}

func TestIntegrationOpenClosedPort(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := Open(ctx, Config{DSN: "postgres://u:p@127.0.0.1:1/db?connect_timeout=1"})
	if err == nil || !strings.HasPrefix(err.Error(), "ping postgres: ") {
		t.Fatalf("got %v", err)
	}
}

func TestIntegrationCommit(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if err := db.Transaction(ctx, func(tx *DB) error { return writeBoth(ctx, tx) }); err != nil {
		t.Fatal(err)
	}
	if n := countItems(t, db); n != 2 {
		t.Fatalf("rows = %d, want 2", n)
	}
}

func TestIntegrationRollbackOnError(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	errDomain := errors.New("domain failure")
	err := db.Transaction(ctx, func(tx *DB) error {
		if err := writeBoth(ctx, tx); err != nil {
			return err
		}
		return errDomain
	})
	if err != errDomain {
		t.Fatalf("callback error must be returned unwrapped, got %v", err)
	}
	if n := countItems(t, db); n != 0 {
		t.Fatalf("rows = %d, want 0", n)
	}
}

func TestIntegrationRollbackOnPanic(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	func() {
		defer func() {
			if r := recover(); r != "boom" {
				t.Fatalf("panic must propagate, got %v", r)
			}
		}()
		_ = db.Transaction(ctx, func(tx *DB) error {
			if err := writeBoth(ctx, tx); err != nil {
				return err
			}
			panic("boom")
		})
	}()
	if n := countItems(t, db); n != 0 {
		t.Fatalf("rows = %d, want 0", n)
	}
	if err := db.Ping(ctx); err != nil {
		t.Fatalf("pool unusable after panic: %v", err)
	}
}

func TestIntegrationSameTransaction(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	err := db.Transaction(ctx, func(tx *DB) error {
		first, err := txID(ctx, tx.Executor)
		if err != nil {
			return err
		}
		query, args, err := tx.Query.Select("txid_current()").ToSql()
		if err != nil {
			return err
		}
		var second int64
		if err := tx.Executor.QueryRow(ctx, query, args...).Scan(&second); err != nil {
			return err
		}
		if first != second {
			t.Errorf("txid differs across statements: %d vs %d", first, second)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestIntegrationNestedAndShutdownInTx(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	err := db.Transaction(ctx, func(tx *DB) error {
		if err := insertItem(ctx, tx.Executor, "outer"); err != nil {
			return err
		}
		if err := tx.Shutdown(ctx); !errors.Is(err, ErrShutdownInTransaction) {
			t.Errorf("tx.Shutdown: got %v", err)
		}
		return tx.Transaction(ctx, func(*DB) error { return nil })
	})
	if !errors.Is(err, ErrNestedTransaction) {
		t.Fatalf("got %v", err)
	}
	if n := countItems(t, db); n != 0 {
		t.Fatalf("outer not rolled back: rows = %d", n)
	}
}

func TestIntegrationTxOptions(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	err := db.TransactionWithOptions(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx *DB) error {
		var level string
		if err := tx.Executor.QueryRow(ctx, "SHOW transaction_isolation").Scan(&level); err != nil {
			return err
		}
		if level != "serializable" {
			t.Errorf("isolation = %q", level)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestIntegrationContextCancellation(t *testing.T) {
	db := openTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := db.Executor.Exec(ctx, "SELECT pg_sleep(10)")
	if err == nil || time.Since(start) > 5*time.Second {
		t.Fatalf("err=%v elapsed=%s", err, time.Since(start))
	}
}

func TestIntegrationSquirrelDynamicQuery(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	for _, name := range []string{"alice", "bob"} {
		if err := insertItem(ctx, db.Executor, name); err != nil {
			t.Fatal(err)
		}
	}
	query, args, err := db.Query.Select("name").From("nforge_items").Where(sq.Eq{"name": "bob"}).ToSql()
	if err != nil {
		t.Fatal(err)
	}
	rows, err := db.Executor.Query(ctx, query, args...)
	if err != nil {
		t.Fatal(err)
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil || len(names) != 1 || names[0] != "bob" {
		t.Fatalf("got %v, %v", names, err)
	}
}

func TestIntegrationShutdown(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if err := db.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.Ping(ctx); err == nil {
		t.Fatal("ping after shutdown must fail")
	}
	if err := db.Shutdown(ctx); err != nil {
		t.Fatalf("second shutdown: %v", err)
	}
}
