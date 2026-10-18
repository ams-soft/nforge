package database

import (
	"context"

	"github.com/ams-soft/nforge/database/internal/rlsexec"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DBTX is the common execution contract satisfied by both *pgxpool.Pool and
// pgx.Tx. Everything above this interface is agnostic to whether it runs on
// the pool or inside a transaction.
type DBTX interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	CopyFrom(ctx context.Context, tableName pgx.Identifier, columnNames []string, rowSrc pgx.CopyFromSource) (int64, error)
	SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults
}

// Compile-time guarantees. If a pgx upgrade ever changes a signature, the
// build fails here instead of at a distant call site.
var (
	_ DBTX = (*pgxpool.Pool)(nil)
	_ DBTX = (pgx.Tx)(nil)
)

var _ DBTX = (*rlsexec.Executor)(nil)
