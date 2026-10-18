package database

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// NewPoolConfig parses the connection string and applies the Nano Forge pool
// and connection overrides. Only positive values override; zero keeps the pgx
// default or the value embedded in the DSN.
//
// Advanced callers may customize the returned config (tracer, AfterConnect,
// type registration), build the pool themselves and hand it to New.
func NewPoolConfig(cfg Config) (*pgxpool.Config, error) {
	dsn, err := cfg.ConnectionString()
	if err != nil {
		return nil, fmt.Errorf("build database connection string: %w", err)
	}

	poolConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse postgres pool config: %w", err)
	}

	if err := validateNonNegative(cfg); err != nil {
		return nil, err
	}

	if cfg.Pool.MinConns > 0 {
		poolConfig.MinConns = cfg.Pool.MinConns
	}
	if cfg.Pool.MaxConns > 0 {
		poolConfig.MaxConns = cfg.Pool.MaxConns
	}
	if cfg.Pool.MaxConnLifetime > 0 {
		poolConfig.MaxConnLifetime = cfg.Pool.MaxConnLifetime
	}
	if cfg.Pool.MaxConnIdleTime > 0 {
		poolConfig.MaxConnIdleTime = cfg.Pool.MaxConnIdleTime
	}
	if cfg.Pool.HealthCheckPeriod > 0 {
		poolConfig.HealthCheckPeriod = cfg.Pool.HealthCheckPeriod
	}
	if cfg.Connection.ConnectTimeout > 0 {
		poolConfig.ConnConfig.ConnectTimeout = cfg.Connection.ConnectTimeout
	}

	if poolConfig.MinConns > poolConfig.MaxConns {
		return nil, fmt.Errorf(
			"database.pool.min_conns (%d) must not exceed database.pool.max_conns (%d)",
			poolConfig.MinConns, poolConfig.MaxConns,
		)
	}

	return poolConfig, nil
}

func validateNonNegative(cfg Config) error {
	checks := []struct {
		name     string
		negative bool
	}{
		{"database.pool.min_conns", cfg.Pool.MinConns < 0},
		{"database.pool.max_conns", cfg.Pool.MaxConns < 0},
		{"database.pool.max_conn_lifetime", cfg.Pool.MaxConnLifetime < 0},
		{"database.pool.max_conn_idle_time", cfg.Pool.MaxConnIdleTime < 0},
		{"database.pool.health_check_period", cfg.Pool.HealthCheckPeriod < 0},
		{"database.connection.connect_timeout", cfg.Connection.ConnectTimeout < 0},
	}
	for _, c := range checks {
		if c.negative {
			return fmt.Errorf("%s must not be negative", c.name)
		}
	}
	return nil
}

// NewPool builds the pool and pings PostgreSQL so that an unreachable or
// misconfigured database fails at startup. On ping failure the pool is closed.
func NewPool(ctx context.Context, cfg Config) (*pgxpool.Pool, error) {
	poolConfig, err := NewPoolConfig(cfg)
	if err != nil {
		return nil, err
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("create postgres pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	return pool, nil
}
