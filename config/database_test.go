package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDatabaseConfigYAMLWithEnvExpansion(t *testing.T) {
	t.Setenv("NF_TEST_DB_PASSWORD", "p@ss: #1")
	t.Setenv("NF_TEST_DB_PORT", "6543")
	t.Setenv("NF_TEST_DATABASE_URL", "")

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	yamlText := `
database:
  enabled: true
  dsn: ${NF_TEST_DATABASE_URL}
  host: localhost
  port: ${NF_TEST_DB_PORT}
  name: nano_forge
  user: postgres
  password: ${NF_TEST_DB_PASSWORD}
  connection:
    connect_timeout: 5s
  pool:
    max_conns: 20
    max_conn_idle_time: 30m
`
	if err := os.WriteFile(path, []byte(yamlText), 0o600); err != nil {
		t.Fatal(err)
	}

	var cfg Config
	if err := loadYAMLFile(path, &cfg); err != nil {
		t.Fatal(err)
	}
	db := cfg.Database
	if !db.Enabled || db.DSN != "" || db.Port != 6543 || db.Password != "p@ss: #1" ||
		db.Connection.ConnectTimeout != 5*time.Second ||
		db.Pool.MaxConns != 20 || db.Pool.MaxConnIdleTime != 30*time.Minute {
		t.Fatalf("unexpected database config: %+v", db)
	}
}

func TestDatabaseConfigEnvFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	env := "DATABASE_PORT=5433\nDATABASE_POOL_MAX_CONN_LIFETIME=2h\nDATABASE_POOL_MIN_CONNS=3\n"
	if err := os.WriteFile(path, []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}

	var cfg Config
	if err := applyEnvFile(path, &cfg); err != nil {
		t.Fatal(err)
	}
	db := cfg.Database
	if db.Port != 5433 || db.Pool.MaxConnLifetime != 2*time.Hour || db.Pool.MinConns != 3 {
		t.Fatalf("unexpected database config: %+v", db)
	}

	if err := os.WriteFile(path, []byte("DATABASE_PORT=70000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := applyEnvFile(path, &cfg); err == nil {
		t.Error("expected overflow error for uint16 port")
	}
}
