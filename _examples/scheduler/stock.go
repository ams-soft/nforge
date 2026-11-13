package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/ams-soft/nforge/database"
	"github.com/ams-soft/nforge/module"
	"github.com/ams-soft/nforge/scheduler"
)

// stockModule is the domain module. It declares the database and the scheduler
// as requirements and registers its job in Init, before the scheduler starts.
// An invalid expression is returned from Init, so it stops the startup.
func stockModule(frequency string) module.Descriptor {
	return module.New("stock", module.Definition{
		Requires: []module.Handle{database.Capability, scheduler.Capability},
		Optional: []module.Handle{module.Logger},
		Init: func(_ context.Context, sc *module.Scope) (module.Hooks, error) {
			db := module.Require(sc, database.Capability)
			sched := module.Require(sc, scheduler.Capability)
			logger, ok := module.Lookup(sc, module.Logger)
			if !ok {
				logger = slog.New(slog.DiscardHandler)
			}
			if err := sched.AddJob(frequency, countStock(db, logger)); err != nil {
				return module.Hooks{}, fmt.Errorf("stock: register the job: %w", err)
			}
			return module.Hooks{}, nil
		},
	})
}

// countStock is the job. The scheduler decides when it runs; what it counts and
// for which period is the job's business. ctx is canceled when the application
// stops, so the query is abandoned instead of holding the pool open.
func countStock(db *database.DB, logger *slog.Logger) scheduler.Handler {
	return func(ctx context.Context) error {
		start := time.Now()
		total, err := stockTotal(ctx, db)
		if err != nil {
			logger.ErrorContext(ctx, "[StockModule] Stock count failed", "error", err, "duration", time.Since(start))
			return err
		}
		logger.InfoContext(ctx, "[StockModule] Stock count", "total", total, "duration", time.Since(start))
		return nil
	}
}

// stockTotal is SELECT COUNT(*) FROM stock, built with Squirrel and run through
// the framework's Executor (never a hand-opened pool).
func stockTotal(ctx context.Context, db *database.DB) (int64, error) {
	sql, args, err := db.Query.Select("COUNT(*)").From("stock").ToSql()
	if err != nil {
		return 0, fmt.Errorf("build the count query: %w", err)
	}
	var total int64
	if err := db.Executor.QueryRow(ctx, sql, args...).Scan(&total); err != nil {
		return 0, fmt.Errorf("count stock: %w", err)
	}
	return total, nil
}
