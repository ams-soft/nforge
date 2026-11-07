//go:build integration

package main

import (
	"fmt"
	"os"
	"testing"

	"github.com/ams-soft/nforge/app"
	nhttp "github.com/ams-soft/nforge/http"
)

// TestDatabaseRoutes runs the /db/notes routes against a real PostgreSQL:
//
//	NFORGE_TEST_DATABASE_URL=postgres://app:app@localhost:5432/app?sslmode=disable \
//	  go test -tags integration ./_examples/basic
func TestDatabaseRoutes(t *testing.T) {
	dsn := os.Getenv("NFORGE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("NFORGE_TEST_DATABASE_URL not set")
	}
	s, _ := app.Value(startApp(t, "DATABASE_DSN="+dsn+"\n"), nhttp.Capability)

	rec := call(t, s, "POST", "/db/notes", "application/json", `{"text":"hello"}`)
	if rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	created := data(t, rec).(map[string]any)
	path := fmt.Sprintf("/db/notes/%d", int64(created["id"].(float64)))
	t.Cleanup(func() { call(t, s, "DELETE", path, "", "") })

	if rec := call(t, s, "POST", "/db/notes", "application/json", `{}`); rec.Code != 400 {
		t.Fatalf("missing text: %d", rec.Code)
	}
	rec = call(t, s, "GET", path, "", "")
	if rec.Code != 200 || data(t, rec).(map[string]any)["text"] != "hello" {
		t.Fatalf("get: %d %s", rec.Code, rec.Body)
	}
	if rec := call(t, s, "GET", "/db/notes", "", ""); rec.Code != 200 {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	if rec := call(t, s, "DELETE", path, "", ""); rec.Code != 204 {
		t.Fatalf("delete: %d", rec.Code)
	}
	for _, p := range []string{path, "/db/notes/99999999999999999999"} {
		if rec := call(t, s, "GET", p, "", ""); rec.Code != 404 {
			t.Fatalf("GET %s: %d", p, rec.Code)
		}
	}
	if rec := call(t, s, "DELETE", path, "", ""); rec.Code != 404 {
		t.Fatalf("delete again: %d", rec.Code)
	}
	ready, _ := data(t, call(t, s, "GET", "/ready", "", "")).(map[string]any)
	if modules, _ := ready["modules"].(map[string]any); modules["database"] != "ready" {
		t.Fatalf("ready: %v", ready)
	}
}
