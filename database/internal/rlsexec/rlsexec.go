// Package rlsexec is the Executor of a tenant-scoped database.DB in rls
// mode: every statement runs with the tenant setting applied
// transaction-locally.
package rlsexec

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SetTenantSQL sets the tenant only until the end of the current transaction,
// so a pooled connection never carries one tenant into another request.
const SetTenantSQL = "SELECT set_config($1, $2, true)"

// Executor is the Executor of a tenant-scoped DB in rls mode. Outside an
// explicit transaction each call sends set_config and the statement as one
// pgx batch: a batch is a single implicit transaction, so the setting covers
// exactly that statement and costs no extra round trip. CopyFrom cannot be
// batched and uses an explicit transaction instead; note that PostgreSQL
// rejects COPY FROM into tables with row level security (use INSERT or
// SendBatch there).
type Executor struct {
	Pool     *pgxpool.Pool
	Setting  string // custom GUC, e.g. app.tenant_id
	TenantID string
}

func (e *Executor) batch(sql string, args []any) *pgx.Batch {
	b := &pgx.Batch{}
	b.Queue(SetTenantSQL, e.Setting, e.TenantID)
	b.Queue(sql, args...)
	return b
}

func (e *Executor) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	br := e.Pool.SendBatch(ctx, e.batch(sql, args))
	if _, err := br.Exec(); err != nil {
		_ = br.Close()
		return pgconn.CommandTag{}, fmt.Errorf("set tenant: %w", err)
	}
	tag, err := br.Exec()
	if closeErr := br.Close(); err == nil {
		err = closeErr
	}
	return tag, err
}

func (e *Executor) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	br := e.Pool.SendBatch(ctx, e.batch(sql, args))
	if _, err := br.Exec(); err != nil {
		_ = br.Close()
		return nil, fmt.Errorf("set tenant: %w", err)
	}
	rows, err := br.Query()
	if err != nil {
		_ = br.Close()
		return nil, err
	}
	return &batchRows{Rows: rows, br: br}, nil
}

func (e *Executor) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	br := e.Pool.SendBatch(ctx, e.batch(sql, args))
	if _, err := br.Exec(); err != nil {
		_ = br.Close()
		return errRow{fmt.Errorf("set tenant: %w", err)}
	}
	return &batchRow{br: br}
}

func (e *Executor) SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults {
	set := &pgx.Batch{}
	set.Queue(SetTenantSQL, e.Setting, e.TenantID)
	scoped := &pgx.Batch{QueuedQueries: append(set.QueuedQueries, b.QueuedQueries...)}
	br := e.Pool.SendBatch(ctx, scoped)
	if _, err := br.Exec(); err != nil {
		_ = br.Close()
		return errBatch{fmt.Errorf("set tenant: %w", err)}
	}
	return br
}

func (e *Executor) CopyFrom(ctx context.Context, table pgx.Identifier, columns []string, src pgx.CopyFromSource) (n int64, err error) {
	tx, err := e.Pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, SetTenantSQL, e.Setting, e.TenantID); err != nil {
		return 0, fmt.Errorf("set tenant: %w", err)
	}
	if n, err = tx.CopyFrom(ctx, table, columns, src); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit transaction: %w", err)
	}
	return n, nil
}

// batchRows closes the batch (releasing the connection and committing the
// implicit transaction) as soon as the rows are exhausted or closed, and
// reports a commit failure through Err.
type batchRows struct {
	pgx.Rows
	br     pgx.BatchResults
	closed bool
	err    error
}

func (r *batchRows) Next() bool {
	if r.closed {
		return false
	}
	if r.Rows.Next() {
		return true
	}
	r.Close()
	return false
}

func (r *batchRows) Close() {
	if r.closed {
		return
	}
	r.closed = true
	r.Rows.Close()
	r.err = r.br.Close()
}

func (r *batchRows) Err() error {
	if err := r.Rows.Err(); err != nil {
		return err
	}
	return r.err
}

type batchRow struct{ br pgx.BatchResults }

func (r *batchRow) Scan(dest ...any) error {
	err := r.br.QueryRow().Scan(dest...)
	if closeErr := r.br.Close(); err == nil {
		err = closeErr
	}
	return err
}

type errRow struct{ err error }

func (r errRow) Scan(...any) error { return r.err }

type errBatch struct{ err error }

func (b errBatch) Exec() (pgconn.CommandTag, error) { return pgconn.CommandTag{}, b.err }
func (b errBatch) Query() (pgx.Rows, error)         { return nil, b.err }
func (b errBatch) QueryRow() pgx.Row                { return errRow(b) }
func (b errBatch) Close() error                     { return b.err }
