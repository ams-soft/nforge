//go:build integration

package main

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/ams-soft/nforge/app"
	"github.com/ams-soft/nforge/database"
	"github.com/ams-soft/nforge/scheduler"
)

// Against a real PostgreSQL (NFORGE_TEST_DATABASE_URL): the schema is applied
// as the example documents, the job counts through the application's database,
// and the scheduler fires it in the configured zone and stops before the pool.
func TestStockJobAgainstPostgres(t *testing.T) {
	dsn := os.Getenv("NFORGE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("NFORGE_TEST_DATABASE_URL is not set")
	}
	cfg := load(t, "app: {name: scheduler-it, version: 1.0.0}\ndatabase: {enabled: true, dsn: '"+dsn+"'}\nscheduler: {timezone: America/Sao_Paulo}\n")
	a, err := newApp(cfg, "* * * * *", app.WithLogger(slog.New(slog.DiscardHandler)))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	stopped := false
	defer func() {
		if !stopped {
			_ = a.Shutdown(context.Background())
		}
	}()
	db, _ := app.Value(a, database.Capability)
	schema, err := os.ReadFile("schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Executor.Exec(context.Background(), string(schema)); err != nil {
		t.Fatal(err)
	}
	total, err := stockTotal(context.Background(), db)
	if err != nil || total < 3 {
		t.Fatalf("stockTotal = %d, %v", total, err)
	}
	s, _ := app.Value(a, scheduler.Capability)
	if s.Location().String() != "America/Sao_Paulo" {
		t.Fatalf("zone = %v", s.Location())
	}
	if !a.Status(context.Background()).Ready {
		t.Fatal("not ready")
	}
	if err := a.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	stopped = true
}
