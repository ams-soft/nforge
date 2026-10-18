//go:build integration

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ams-soft/nforge/config"
	"github.com/ams-soft/nforge/deps"
	nhttp "github.com/ams-soft/nforge/http"
)

// newTestServer runs the example against a real PostgreSQL (column mode):
//
//	NFORGE_TEST_DATABASE_URL=postgres://app:app@localhost:5432/app?sslmode=disable \
//	  go test -tags integration ./_examples/database
func newTestServer(t *testing.T) *nhttp.ServerHttp {
	t.Helper()
	dsn := os.Getenv("NFORGE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("NFORGE_TEST_DATABASE_URL not set")
	}
	env := filepath.Join(t.TempDir(), "test.env")
	content := "DATABASE_DSN=" + dsn + "\nLOGGING_OUTPUT_TERMINAL_ENABLED=false\nHTTP_SERVER_HEADER=false\n"
	if err := os.WriteFile(env, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFlags("config.yaml", env)
	if err != nil {
		t.Fatal(err)
	}
	dps := deps.NewDeps(*cfg)
	t.Cleanup(func() { _ = dps.Shutdown(context.Background()) })
	server, err := newServer(dps)
	if err != nil {
		t.Fatal(err)
	}
	return server
}

type result struct {
	status int
	data   any
	raw    string
}

func call(t *testing.T, s *nhttp.ServerHttp, method, path, tenant, body string) result {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if tenant != "" {
		req.Header.Set("X-Tenant-ID", tenant)
	}
	rec := httptest.NewRecorder()
	s.Srv.Handler.ServeHTTP(rec, req)
	var env struct {
		Data any `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	return result{rec.Code, env.Data, rec.Body.String()}
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// randomTenant returns a fresh UUID v4, so reruns never see old rows.
func randomTenant() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b)
	return fmt.Sprintf("%s-%s-%s-%s-%s", h[:8], h[8:12], h[12:16], h[16:20], h[20:])
}

func TestIntegrationUsers(t *testing.T) {
	s := newTestServer(t)
	tag := randomHex(4) // unique names: the table outlives the test

	r := call(t, s, "POST", "/api/v1/users", "", `{"name":"ana-`+tag+`","email":"ana@example.com"}`)
	if r.status != 201 {
		t.Fatalf("create: %d %s", r.status, r.raw)
	}
	id := strconv.FormatInt(int64(r.data.(map[string]any)["id"].(float64)), 10)

	if r := call(t, s, "POST", "/api/v1/users/batch", "", `[{"name":"bia-`+tag+`"},{"name":"caio-`+tag+`","active":false}]`); r.status != 201 || len(r.data.([]any)) != 2 {
		t.Fatalf("batch: %d %s", r.status, r.raw)
	}
	list := func(query string) int {
		r := call(t, s, "GET", "/api/v1/users?limit=100&name="+tag+query, "", "")
		if r.status != 200 {
			t.Fatalf("list: %d %s", r.status, r.raw)
		}
		return len(r.data.([]any))
	}
	if n := list(""); n != 3 {
		t.Errorf("users with the tag: %d", n)
	}
	if n := list("&active=false"); n != 1 {
		t.Errorf("inactive users: %d", n)
	}

	if r := call(t, s, "PUT", "/api/v1/users/"+id, "", `{"name":"ana-maria-`+tag+`"}`); r.status != 200 || r.data.(map[string]any)["name"] != "ana-maria-"+tag {
		t.Fatalf("update: %d %s", r.status, r.raw)
	}
	if r := call(t, s, "DELETE", "/api/v1/users/"+id, "", ""); r.status != 204 {
		t.Fatalf("delete: %d %s", r.status, r.raw)
	}
	for _, method := range []string{"GET", "DELETE"} {
		if r := call(t, s, method, "/api/v1/users/"+id, "", ""); r.status != 404 {
			t.Errorf("%s deleted user: %d", method, r.status)
		}
	}
	if r := call(t, s, "GET", "/ready", "", ""); r.status != 200 {
		t.Errorf("ready: %d %s", r.status, r.raw)
	}
}

func TestIntegrationNotesAreIsolatedByTenant(t *testing.T) {
	s := newTestServer(t)
	a, b := randomTenant(), randomTenant()

	r := call(t, s, "POST", "/api/v1/notes", a, `{"text":"from A"}`)
	if r.status != 201 {
		t.Fatalf("create A: %d %s", r.status, r.raw)
	}
	noteA := r.data.(map[string]any)
	if noteA["tenant_id"] != a {
		t.Fatalf("note tenant = %v", noteA["tenant_id"])
	}
	idA := strconv.FormatInt(int64(noteA["id"].(float64)), 10)

	if r := call(t, s, "POST", "/api/v1/notes/batch", b, `[{"text":"b1"},{"text":"b2"}]`); r.status != 201 {
		t.Fatalf("batch B: %d %s", r.status, r.raw)
	}

	count := func(path, tenant string) int {
		r := call(t, s, "GET", path, tenant, "")
		if r.status != 200 {
			t.Fatalf("GET %s: %d %s", path, r.status, r.raw)
		}
		return len(r.data.([]any))
	}
	if n := count("/api/v1/notes", a); n != 1 {
		t.Errorf("tenant A sees %d notes", n)
	}
	if n := count("/api/v1/notes", b); n != 2 {
		t.Errorf("tenant B sees %d notes", n)
	}
	if n := count("/raw/notes", b); n != 2 {
		t.Errorf("raw route: tenant B sees %d notes", n)
	}
	if r := call(t, s, "GET", "/api/v1/notes/"+idA, a, ""); r.status != 200 {
		t.Errorf("A reads its note: %d", r.status)
	}
	if r := call(t, s, "GET", "/api/v1/notes/"+idA, b, ""); r.status != 404 {
		t.Errorf("B reads A's note: %d, want 404", r.status)
	}

	for _, tc := range []struct {
		name, path, tenant string
		status             int
	}{
		{"typed without tenant", "/api/v1/notes", "", 403},
		{"raw without tenant", "/raw/notes", "", 403},
		{"typed invalid tenant", "/api/v1/notes", "acme", 400},
		{"raw invalid tenant", "/raw/notes", "acme", 400},
	} {
		if r := call(t, s, "GET", tc.path, tc.tenant, ""); r.status != tc.status {
			t.Errorf("%s: %d, want %d", tc.name, r.status, tc.status)
		}
	}
}
