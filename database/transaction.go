package database

import (
	"context"
	"fmt"

	"github.com/ams-soft/nforge/database/internal/rlsexec"

	"github.com/jackc/pgx/v5"
)

// Transaction runs fn inside a transaction with the server's default options.
// See TransactionWithOptions.
func (db *DB) Transaction(ctx context.Context, fn func(tx *DB) error) error {
	return db.TransactionWithOptions(ctx, pgx.TxOptions{}, fn)
}

// TransactionWithOptions begins a transaction, hands fn a transaction-scoped
// DB whose Executor is the pgx.Tx, and commits if fn returns nil. Any error or
// panic from fn rolls back; fn's error is returned unwrapped. Nested
// transactions return ErrNestedTransaction.
//
// Inside fn, use the tx argument — never the outer db, which would run on a
// different pool connection outside the transaction.
//
// On a tenant-scoped DB in rls mode the tenant setting is applied right after
// BEGIN and lasts until COMMIT/ROLLBACK; tx keeps the tenant (TenantID,
// TenantEq).
func (db *DB) TransactionWithOptions(
	ctx context.Context,
	options pgx.TxOptions,
	fn func(tx *DB) error,
) error {
	if db.transaction {
		return ErrNestedTransaction
	}

	tx, err := db.pool.BeginTx(ctx, options)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}

	// Runs on every exit path; after a successful commit it returns
	// pgx.ErrTxClosed, which is deliberately ignored. Panics are not recovered.
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	if db.tenantID != "" && db.tenancy.Mode == TenancyRLS {
		if _, err := tx.Exec(ctx, rlsexec.SetTenantSQL, db.tenancy.Setting, db.tenantID); err != nil {
			return fmt.Errorf("set tenant: %w", err)
		}
	}

	txDB := &DB{
		Executor:    tx,
		Query:       newStatementBuilder(),
		pool:        db.pool,
		transaction: true,
		tenancy:     db.tenancy,
		tenantID:    db.tenantID,
	}

	if err := fn(txDB); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}

	return nil
}
