package main

import (
	"log/slog"
	"testing"

	"github.com/ams-soft/nforge/app"
	"github.com/ams-soft/nforge/config"
	"github.com/ams-soft/nforge/database"
	nhttp "github.com/ams-soft/nforge/http"
	"github.com/ams-soft/nforge/module"
)

// validated builds the application of a configuration (no I/O: nothing is
// opened) and returns the validated database and http sections.
func validated(t *testing.T, envFile string) (database.Config, nhttp.Config) {
	t.Helper()
	cfg, err := config.LoadFlags("config.yaml", envFile)
	if err != nil {
		t.Fatal(err)
	}
	var db database.Config
	var web nhttp.Config
	reader := module.New("reader", module.Definition{
		Optional: []module.Handle{database.Capability, nhttp.Capability},
		Check: func(v *module.View) error {
			var err error
			if db, _, _, err = module.ConfigOf[database.Config](v, database.Capability); err != nil {
				return err
			}
			web, _, _, err = module.ConfigOf[nhttp.Config](v, nhttp.Capability)
			return err
		},
	})
	if _, err := newApp(cfg, app.Modules(reader), app.WithLogger(slog.New(slog.DiscardHandler))); err != nil {
		t.Fatal(err)
	}
	return db, web
}

// The routes need PostgreSQL (see integration_test.go); without it this only
// checks that both configurations of the example load and validate.
func TestConfigurations(t *testing.T) {
	db, web := validated(t, "")
	if !db.Enabled || db.Tenancy.Mode != database.TenancyColumn || web.Tenant.Source != "header" {
		t.Fatalf("column mode with the header source expected: %+v", db.Tenancy)
	}
	rls, _ := validated(t, "rls.env")
	if rls.Tenancy.Mode != database.TenancyRLS || rls.User != "app_tenant" {
		t.Fatalf("rls.env must switch to rls with the app_tenant role: %+v", rls.Tenancy)
	}
}
