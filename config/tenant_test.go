package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ams-soft/nforge/database"
)

func loadTenantConfig(t *testing.T, yamlText, envText string) (*Config, error) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("app: {name: t, version: 1.0.0}\n"+yamlText), 0o600); err != nil {
		t.Fatal(err)
	}
	envPath := ""
	if envText != "" {
		envPath = filepath.Join(dir, ".env")
		if err := os.WriteFile(envPath, []byte(envText), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return LoadFlags(path, envPath)
}

func TestTenantDefaults(t *testing.T) {
	cfg, err := loadTenantConfig(t, "http: {server: {port: 8080}}", "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTP.Tenant.Source != "claim" || cfg.HTTP.Tenant.Header != "X-Tenant-ID" {
		t.Fatalf("http.tenant = %+v", cfg.HTTP.Tenant)
	}
	if cfg.Database.Tenancy.Enabled {
		t.Fatal("database.tenancy must be disabled by default")
	}
}

func TestTenancyYAMLAndEnvFile(t *testing.T) {
	yamlText := `
http: {server: {port: 8080}}
database:
  enabled: true
  dsn: postgres://u:p@localhost/db
  tenancy:
    enabled: true
    mode: rls
    column: org_id
    tables: [users]
`
	cfg, err := loadTenantConfig(t, yamlText, "DATABASE_TENANCY_TABLES=users,billing.invoices\nHTTP_TENANT_SOURCE=header\nHTTP_TENANT_HEADER=X-Org\n")
	if err != nil {
		t.Fatal(err)
	}
	ten := cfg.Database.Tenancy
	if !ten.Enabled || ten.Mode != database.TenancyRLS || ten.Column != "org_id" ||
		strings.Join(ten.Tables, ",") != "users,billing.invoices" {
		t.Fatalf("database.tenancy = %+v", ten)
	}
	if cfg.HTTP.Tenant.Source != "header" || cfg.HTTP.Tenant.Header != "X-Org" {
		t.Fatalf("http.tenant = %+v", cfg.HTTP.Tenant)
	}
}

func TestTenantValidation(t *testing.T) {
	tests := map[string]struct {
		yaml, want string
	}{
		"source":        {"http: {server: {port: 8080}, tenant: {source: cookie}}", "http.tenant.source"},
		"header":        {"http: {server: {port: 8080}, tenant: {source: header, header: \"X Tenant\"}}", "http.tenant.header"},
		"tenancy mode":  {"http: {server: {port: 8080}}\ndatabase: {enabled: true, tenancy: {enabled: true}}", "database.tenancy.mode"},
		"tenancy table": {"http: {server: {port: 8080}}\ndatabase: {enabled: true, tenancy: {enabled: true, mode: column, tables: [\"a;b\"]}}", "database.tenancy.tables"},
	}
	for name, tt := range tests {
		_, err := loadTenantConfig(t, tt.yaml, "")
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: err = %v, want it to mention %s", name, err, tt.want)
		}
		if strings.HasPrefix(name, "tenancy") && !errors.Is(err, database.ErrInvalidTenancy) {
			t.Errorf("%s: err = %v, want ErrInvalidTenancy", name, err)
		}
	}

	// tenancy is only validated when the database is enabled
	if _, err := loadTenantConfig(t, "http: {server: {port: 8080}}\ndatabase: {enabled: false, tenancy: {enabled: true}}", ""); err != nil {
		t.Errorf("disabled database: %v", err)
	}
}
