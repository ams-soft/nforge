package database

import (
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const testDSN = "postgres://u:secret@localhost:5432/db"

func TestNewPoolConfigKeepsDefaults(t *testing.T) {
	got, err := NewPoolConfig(Config{DSN: testDSN})
	if err != nil {
		t.Fatal(err)
	}
	want, _ := pgxpool.ParseConfig(testDSN)
	if got.MinConns != want.MinConns || got.MaxConns != want.MaxConns ||
		got.MaxConnLifetime != want.MaxConnLifetime || got.MaxConnIdleTime != want.MaxConnIdleTime ||
		got.HealthCheckPeriod != want.HealthCheckPeriod ||
		got.ConnConfig.ConnectTimeout != want.ConnConfig.ConnectTimeout {
		t.Errorf("zero overrides changed pgx defaults")
	}
}

func TestNewPoolConfigOverrides(t *testing.T) {
	got, err := NewPoolConfig(Config{
		DSN:        testDSN,
		Connection: ConnectionConfig{ConnectTimeout: 5 * time.Second},
		Pool: PoolConfig{
			MinConns:          2,
			MaxConns:          20,
			MaxConnLifetime:   2 * time.Hour,
			MaxConnIdleTime:   10 * time.Minute,
			HealthCheckPeriod: 30 * time.Second,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.MinConns != 2 || got.MaxConns != 20 || got.MaxConnLifetime != 2*time.Hour ||
		got.MaxConnIdleTime != 10*time.Minute || got.HealthCheckPeriod != 30*time.Second ||
		got.ConnConfig.ConnectTimeout != 5*time.Second {
		t.Errorf("overrides not applied: %+v", got)
	}
}

func TestNewPoolConfigDSNEmbeddedValues(t *testing.T) {
	dsn := testDSN + "?pool_max_conns=7"
	got, err := NewPoolConfig(Config{DSN: dsn})
	if err != nil || got.MaxConns != 7 {
		t.Fatalf("DSN value not preserved: %v %v", got.MaxConns, err)
	}
	got, err = NewPoolConfig(Config{DSN: dsn, Pool: PoolConfig{MaxConns: 12}})
	if err != nil || got.MaxConns != 12 {
		t.Fatalf("YAML must override DSN: %v %v", got.MaxConns, err)
	}
}

func TestNewPoolConfigRejectsInvalid(t *testing.T) {
	bad := []Config{
		{DSN: testDSN, Pool: PoolConfig{MinConns: -1}},
		{DSN: testDSN, Pool: PoolConfig{MaxConns: -1}},
		{DSN: testDSN, Pool: PoolConfig{MaxConnLifetime: -time.Second}},
		{DSN: testDSN, Pool: PoolConfig{MaxConnIdleTime: -time.Second}},
		{DSN: testDSN, Pool: PoolConfig{HealthCheckPeriod: -time.Second}},
		{DSN: testDSN, Connection: ConnectionConfig{ConnectTimeout: -time.Second}},
		{DSN: testDSN, Pool: PoolConfig{MinConns: 10, MaxConns: 5}},
	}
	for i, cfg := range bad {
		if _, err := NewPoolConfig(cfg); err == nil {
			t.Errorf("case %d: expected error", i)
		}
	}
}

func TestNewPoolConfigErrorsDoNotLeakSecrets(t *testing.T) {
	_, err := NewPoolConfig(Config{DSN: "postgres://u:secret@localhost:notaport/db"})
	if err == nil {
		t.Fatal("expected parse error")
	}
	if !strings.HasPrefix(err.Error(), "parse postgres pool config: ") || strings.Contains(err.Error(), "secret") {
		t.Errorf("unexpected error: %v", err)
	}
}

// A statement that runs forever keeps its pooled connection after the client
// is gone; the server must cancel it.
func TestNewPoolConfigSetsStatementTimeout(t *testing.T) {
	timeout := func(cfg Config) string {
		t.Helper()
		got, err := NewPoolConfig(cfg)
		if err != nil {
			t.Fatal(err)
		}
		return got.ConnConfig.RuntimeParams["statement_timeout"]
	}
	if got := timeout(Config{DSN: testDSN}); got != "30000" {
		t.Errorf("default statement_timeout = %q ms, want 30000", got)
	}
	if got := timeout(Config{DSN: testDSN, Connection: ConnectionConfig{StatementTimeout: 90 * time.Second}}); got != "90000" {
		t.Errorf("configured statement_timeout = %q", got)
	}
	if got := timeout(Config{DSN: testDSN + "?statement_timeout=5000"}); got != "5000" {
		t.Errorf("a statement_timeout in the DSN must win, got %q", got)
	}
	if got := timeout(Config{Host: "h", Port: 5432, Name: "d", User: "u"}); got != "30000" {
		t.Errorf("the field-based config gets the default too: %q", got)
	}
	if _, err := NewPoolConfig(Config{DSN: testDSN, Connection: ConnectionConfig{StatementTimeout: -time.Second}}); err == nil {
		t.Error("a negative statement_timeout must be refused")
	}
}
