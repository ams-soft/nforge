package main

import (
	"testing"

	"github.com/ams-soft/nforge/config"
	"github.com/ams-soft/nforge/database"
)

// The routes need PostgreSQL (see integration_test.go); without it this only
// checks that both configurations of the example load and validate.
func TestConfigurations(t *testing.T) {
	cfg, err := config.LoadFlags("config.yaml", "")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Database.Enabled || cfg.Database.Tenancy.Mode != database.TenancyColumn || cfg.HTTP.Tenant.Source != "header" {
		t.Fatalf("column mode with the header source expected: %+v", cfg.Database.Tenancy)
	}

	rls, err := config.LoadFlags("config.yaml", "rls.env")
	if err != nil {
		t.Fatal(err)
	}
	if rls.Database.Tenancy.Mode != database.TenancyRLS || rls.Database.User != "app_tenant" {
		t.Fatalf("rls.env must switch to rls with the app_tenant role: %+v", rls.Database.Tenancy)
	}
}
