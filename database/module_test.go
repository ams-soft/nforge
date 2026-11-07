package database_test

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/ams-soft/nforge/app"
	"github.com/ams-soft/nforge/database"
	"github.com/ams-soft/nforge/internal/apptest"
	"github.com/ams-soft/nforge/module"
)

func TestModuleIsDisabledByDefault(t *testing.T) {
	a := apptest.Start(t, "", database.Module())
	if a.Graph().Modules[0].Enabled {
		t.Fatal("database.enabled defaults to false")
	}
	if _, ok := app.Value(a, database.Capability); ok {
		t.Fatal("a disabled database publishes nothing")
	}
	// a module that requires it fails before anything opens
	user := module.New("users", module.Definition{Requires: []module.Handle{database.Capability}})
	if _, err := apptest.New(t, "", database.Module(), user); !errors.Is(err, module.ErrDisabledDependency) {
		t.Fatalf("err = %v", err)
	}
}

// The section decodes with ${VAR} expansion and the env file, and its typed
// values reach the dependents' Check.
func TestModuleConfiguration(t *testing.T) {
	t.Setenv("NF_TEST_DB_PASSWORD", "p@ss: #1")
	t.Setenv("NF_TEST_DB_PORT", "6543")
	t.Setenv("NF_TEST_DATABASE_URL", "")
	yaml := `
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
	var got database.Config
	reader := module.New("reader", module.Definition{Optional: []module.Handle{database.Capability}, Check: func(v *module.View) error {
		cfg, _, _, err := module.ConfigOf[database.Config](v, database.Capability)
		got = cfg
		return err
	}})
	d := apptest.Load(t, yaml, "DATABASE_POOL_MAX_CONN_LIFETIME=2h\nDATABASE_POOL_MIN_CONNS=3\n")
	if _, err := app.New(d, app.Modules(database.Module(), reader), apptest.Quiet()); err != nil {
		t.Fatal(err)
	}
	if !got.Enabled || got.DSN != "" || got.Port != 6543 || got.Password != "p@ss: #1" ||
		got.Connection.ConnectTimeout != 5*time.Second || got.Pool.MaxConns != 20 ||
		got.Pool.MaxConnIdleTime != 30*time.Minute || got.Pool.MaxConnLifetime != 2*time.Hour || got.Pool.MinConns != 3 {
		t.Fatalf("config = %+v", got)
	}
	d = apptest.Load(t, yaml, "DATABASE_PORT=70000\n")
	if _, err := app.New(d, app.Modules(database.Module()), apptest.Quiet()); err == nil {
		t.Error("a port that overflows uint16 must fail")
	}
}

func TestModuleValidatesBeforeConnecting(t *testing.T) {
	for yaml, want := range map[string]string{
		"database: {enabled: true}\n": "missing",
		"database: {enabled: true, dsn: 'postgres://u@h/db', pool: {min_conns: 5, max_conns: 2}}\n": "must not exceed",
		"database: {enabled: true, dsn: 'postgres://u@h/db', pool: {max_conns: -1}}\n":              "must not be negative",
		"database: {enabled: true, dsn: 'postgres://u@h/db', tenancy: {enabled: true}}\n":           "mode",
		"database: {enabled: true, driver: mysql, dsn: 'x'}\n":                                      "driver",
		"database: {enabled: true, dsn: 'postgres://u@h/db', tenancy: {modee: rls}}\n":              "not found",
		"database: {enabled: false, tenancy: {enabled: true, mode: nope}}\n":                        "",
	} {
		_, err := apptest.New(t, yaml, database.Module())
		if want == "" {
			if err != nil {
				t.Errorf("%s: a disabled database is not validated: %v", yaml, err)
			}
			continue
		}
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), want) {
			t.Errorf("%s: err = %v", yaml, err)
		}
	}
}

func TestModulePlaintextWarning(t *testing.T) {
	remote := "database: {enabled: true, host: db.internal, port: 5432, name: app, user: app, password: s3cr3t}\n"
	a, err := apptest.New(t, remote, database.Module())
	if err != nil {
		t.Fatal(err)
	}
	if w := a.Warnings(); len(w) != 1 || !strings.Contains(w[0], "unset (prefer)") || strings.Contains(w[0], "s3cr3t") {
		t.Fatalf("warnings = %v", w)
	}
	if _, err := apptest.New(t, "env: prd\n"+remote, database.Module()); err == nil || !strings.Contains(err.Error(), "prd_exceptions.database_plaintext") {
		t.Fatalf("prd: err = %v", err)
	}
	for _, safe := range []string{
		"database: {enabled: true, host: localhost, port: 5432, name: app, user: app, password: x}\n",
		"database: {enabled: true, host: db.internal, port: 5432, name: app, user: app, password: x, ssl: {mode: verify-full}}\n",
		"database: {enabled: true, dsn: 'postgres://app:x@db.internal/app?sslmode=require'}\n",
		"database: {enabled: true, dsn: 'host=/var/run/postgresql user=app password=x dbname=app'}\n",
		"database: {enabled: true, host: db.internal, port: 5432, name: app, user: app}\n",
	} {
		a, err := apptest.New(t, safe, database.Module())
		if err != nil || len(a.Warnings()) != 0 {
			t.Errorf("%s: %v %v", safe, err, a.Warnings())
		}
	}
	if !(database.Config{DSN: "host=/tmp user=u"}).RequiresTLS() || (database.Config{Host: "h"}).RequiresTLS() ||
		!(database.Config{Host: "h", SSL: database.SSLConfig{Mode: "verify-ca"}}).RequiresTLS() {
		t.Fatal("RequiresTLS")
	}
}

// An unreachable database fails the start, and nothing stays open.
func TestModuleStartFailureIsFatal(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	a, err := apptest.New(t, "database: {enabled: true, dsn: 'postgres://u:p@"+addr+"/db?sslmode=disable&connect_timeout=1'}\n", database.Module())
	if err != nil {
		t.Fatal(err)
	}
	err = a.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "unable to setup database") || strings.Contains(err.Error(), ":p@") {
		t.Fatalf("err = %v", err)
	}
}
