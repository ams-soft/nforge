//go:build integration

package database

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"testing"
	"time"
)

var adversarialTenants = []string{
	tenantA,
	tenantB,
	"33333333-3333-4333-8333-333333333333",
	"44444444-4444-4444-8444-444444444444",
}

// Tenants share two connections while requests are canceled mid-flight, fail
// with syntax errors, abandon their rows and roll back. No tenant may ever see
// another one's row and no connection may keep a tenant setting afterwards.
func TestIntegrationRLSAdversarialConcurrency(t *testing.T) {
	_, appDSN := rlsFixture(t)
	db := openRLS(t, appDSN, 2) // few connections: every one is reused by every tenant

	var wg sync.WaitGroup
	leaks := make(chan string, 1024)
	for worker := range 16 {
		tenant := adversarialTenants[worker%len(adversarialTenants)]
		scoped := mustTenant(t, db, tenant)
		wg.Go(func() {
			random := rand.New(rand.NewPCG(uint64(worker), 7)) //nolint:gosec // reproducible test schedule
			for round := range 60 {
				adversarialStep(t, scoped, tenant, random.IntN(6), worker, round, leaks)
			}
		})
	}
	wg.Wait()
	close(leaks)
	for leak := range leaks {
		t.Error(leak)
	}

	ctx := context.Background()
	var rootRows int
	if err := db.Executor.QueryRow(ctx, "SELECT count(*) FROM nforge_tenant_items").Scan(&rootRows); err != nil {
		t.Fatal(err)
	}
	if rootRows != 0 {
		t.Errorf("the unscoped connection sees %d tenant rows", rootRows)
	}
	// Hold every pooled connection at once, so each one is inspected.
	conns := make([]interface{ Release() }, 0, 2)
	for range 2 {
		conn, err := db.pool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, conn)
		var setting *string
		if err := conn.QueryRow(ctx, "SELECT NULLIF(current_setting('app.tenant_id', true), '')").Scan(&setting); err != nil {
			t.Fatal(err)
		}
		if setting != nil {
			t.Errorf("a pooled connection kept the tenant %s", *setting)
		}
	}
	for _, c := range conns {
		c.Release()
	}
	for _, tenant := range adversarialTenants {
		var n int
		scoped := mustTenant(t, db, tenant)
		if err := scoped.Executor.QueryRow(ctx, "SELECT count(*) FROM nforge_tenant_items").Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			t.Errorf("tenant %s wrote nothing: the test proved nothing", tenant)
		}
	}
}

func adversarialStep(t *testing.T, db *DB, tenant string, step, worker, round int, leaks chan<- string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	name := fmt.Sprintf("%s/%d/%d", tenant, worker, round)
	switch step {
	case 0: // plain write
		if _, err := db.Executor.Exec(ctx, "INSERT INTO nforge_tenant_items (name) VALUES ($1)", name); err != nil {
			t.Errorf("insert: %v", err)
		}
	case 1: // full read: the isolation check itself
		rows, err := db.Executor.Query(ctx, "SELECT name FROM nforge_tenant_items")
		if err != nil {
			t.Errorf("query: %v", err)
			return
		}
		defer rows.Close()
		for rows.Next() {
			var got string
			if err := rows.Scan(&got); err != nil {
				t.Errorf("scan: %v", err)
				return
			}
			if !strings.HasPrefix(got, tenant+"/") {
				leaks <- fmt.Sprintf("tenant %s read a row of %s", tenant, got)
			}
		}
		if err := rows.Err(); err != nil {
			t.Errorf("rows: %v", err)
		}
	case 2: // canceled while the statement runs
		short, stop := context.WithTimeout(ctx, time.Duration(worker%4+1)*time.Millisecond)
		defer stop()
		_, _ = db.Executor.Exec(short, "SELECT pg_sleep(0.05)")
	case 3: // abandoned after the first row
		rows, err := db.Executor.Query(ctx, "SELECT name FROM nforge_tenant_items")
		if err != nil {
			t.Errorf("query: %v", err)
			return
		}
		rows.Next()
		rows.Close()
	case 4: // the statement after set_config fails
		if _, err := db.Executor.Exec(ctx, "SELECT * FROM nforge_missing_table"); err == nil {
			t.Error("the bad statement succeeded")
		}
	case 5: // a rolled back transaction, and a write for another tenant
		boom := errors.New("boom")
		err := db.Transaction(ctx, func(tx *DB) error {
			if _, err := tx.Executor.Exec(ctx, "INSERT INTO nforge_tenant_items (name) VALUES ($1)", "rolled-back"); err != nil {
				return err
			}
			var other string
			for _, candidate := range adversarialTenants {
				if candidate != tenant {
					other = candidate
					break
				}
			}
			if _, err := tx.Executor.Exec(ctx, "SAVEPOINT s"); err != nil {
				return err
			}
			if _, err := tx.Executor.Exec(ctx, "INSERT INTO nforge_tenant_items (tenant_id, name) VALUES ($1, 'stolen')", other); err == nil {
				leaks <- "the policy accepted a row for another tenant"
			}
			return boom
		})
		if !errors.Is(err, boom) {
			t.Errorf("transaction: %v", err)
		}
	}
}

// A canceled transaction must not return its connection in a transaction.
func TestIntegrationCanceledTransactionLeavesThePoolClean(t *testing.T) {
	_, appDSN := rlsFixture(t)
	db := openRLS(t, appDSN, 1)
	scoped := mustTenant(t, db, tenantA)
	ctx, cancel := context.WithCancel(context.Background())
	err := scoped.Transaction(ctx, func(tx *DB) error {
		if _, err := tx.Executor.Exec(ctx, "INSERT INTO nforge_tenant_items (name) VALUES ('canceled')"); err != nil {
			return err
		}
		cancel()
		_, err := tx.Executor.Exec(ctx, "SELECT 1")
		return err
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	var rows int
	background := context.Background()
	if err := scoped.Executor.QueryRow(background, "SELECT count(*) FROM nforge_tenant_items").Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("the canceled transaction left %d rows", rows)
	}
}
