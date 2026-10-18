package database

import (
	"context"

	sq "github.com/Masterminds/squirrel"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DB exposes two complementary ways of talking to PostgreSQL over one
// executor: Query (Squirrel builder, build only) and Executor (native pgx:
// the pool, or the pgx.Tx inside a transaction).
//
// The zero value is not usable; obtain instances through Open or New.
// A root DB is safe for concurrent use; a transaction-scoped DB is not and
// must not be retained after its callback returns.
type DB struct {
	// Executor is the current pgx executor. Do not reassign it.
	Executor DBTX

	// Query builds SQL with $N placeholders; execute its ToSql() output
	// through Executor. It never holds a runner.
	Query sq.StatementBuilderType

	pool        *pgxpool.Pool
	transaction bool

	tenancy  *TenancyConfig // nil when tenancy is disabled; shared, never mutated
	tenantID string         // set on tenant-scoped DBs (ForTenant/ForContext)
}

// New wraps an existing pool. The returned root DB owns the pool: close it
// only through Shutdown, and never pass the same pool to New twice.
// It panics on a nil pool or an invalid option (programmer errors caught at
// startup). With WithTenancy it does not run CheckTenancy; Open does.
func New(pool *pgxpool.Pool, opts ...Option) *DB {
	if pool == nil {
		panic(ErrNilPool)
	}

	db := &DB{
		Executor: pool,
		Query:    newStatementBuilder(),
		pool:     pool,
	}
	for _, opt := range opts {
		opt(db)
	}
	return db
}

// Open builds a pool from cfg (failing fast if PostgreSQL is unreachable)
// and returns a root DB that owns it. With cfg.Tenancy enabled it also runs
// CheckTenancy, so a database that would not isolate tenants never starts.
func Open(ctx context.Context, cfg Config) (*DB, error) {
	if err := cfg.Tenancy.Validate(); err != nil {
		return nil, err
	}

	pool, err := NewPool(ctx, cfg)
	if err != nil {
		return nil, err
	}

	db := New(pool, WithTenancy(cfg.Tenancy))
	if err := db.CheckTenancy(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return db, nil
}

// Ping checks the pool. On a transaction-scoped DB it still targets the pool,
// not the transaction's connection.
func (db *DB) Ping(ctx context.Context) error {
	return db.pool.Ping(ctx)
}

// Shutdown closes the pool. pgxpool.Close takes no context: it blocks until
// every acquired connection is released, so call it after the services that
// use the database have stopped. Calling it twice is safe.
func (db *DB) Shutdown(ctx context.Context) error {
	if db.transaction {
		return ErrShutdownInTransaction
	}
	if db.tenantID != "" {
		return ErrShutdownTenantScoped
	}

	db.pool.Close()

	return nil
}

func newStatementBuilder() sq.StatementBuilderType {
	return sq.StatementBuilder.PlaceholderFormat(sq.Dollar)
}
