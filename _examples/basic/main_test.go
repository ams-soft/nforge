package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ams-soft/nforge/config"
	"github.com/ams-soft/nforge/deps"
	nhttp "github.com/ams-soft/nforge/http"
)

// newTestServer loads the real config.yaml (quiet, database off as in
// standalone.env) and returns the server with every route, without
// listening on a port. database_integration_test.go covers the database.
func newTestServer(t *testing.T) *nhttp.ServerHttp {
	t.Helper()
	env := filepath.Join(t.TempDir(), "test.env")
	if err := os.WriteFile(env, []byte("DATABASE_ENABLED=false\nLOGGING_OUTPUT_TERMINAL_ENABLED=false\nHTTP_SERVER_HEADER=false\n"), 0o600); err != nil {
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

func call(t *testing.T, s *nhttp.ServerHttp, method, path, contentType, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	s.Srv.Handler.ServeHTTP(rec, req)
	return rec
}

func data(t *testing.T, rec *httptest.ResponseRecorder) any {
	t.Helper()
	var env struct {
		Success bool `json:"success"`
		Data    any  `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("not a JSON envelope: %s", rec.Body.String())
	}
	return env.Data
}

func TestPlainRoutesInEveryFormat(t *testing.T) {
	s := newTestServer(t)

	rec := call(t, s, "POST", "/echo/json", "application/json", `{"id":7,"name":"Ana"}`)
	if rec.Code != 200 || data(t, rec).(map[string]any)["name"] != "Ana" {
		t.Fatalf("json: %d %s", rec.Code, rec.Body)
	}
	if rec.Header().Get("X-Clacks-Overhead") == "" {
		t.Error("the global middleware did not run")
	}

	rec = call(t, s, "POST", "/echo/xml", "application/xml", `<user id="7"><name>Ana</name></user>`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `<data id="7"><name>Ana</name></data>`) {
		t.Fatalf("xml: %d %s", rec.Code, rec.Body)
	}

	rec = call(t, s, "POST", "/echo/cbor", "application/cbor", "\xa1\x64name\x63Ana")
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/cbor" {
		t.Fatalf("cbor: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}

	for _, tc := range []struct {
		name, contentType, body string
		status                  int
	}{
		{"syntax", "application/json", `{"name":`, 400},
		{"missing required", "application/json", `{"email":"a@b.c"}`, 400},
		{"wrong content type", "text/plain", `{"name":"Ana"}`, 415},
		{"validation", "application/json", `{"name":"Ana","email":"nope"}`, 422},
	} {
		if rec := call(t, s, "POST", "/echo/json", tc.contentType, tc.body); rec.Code != tc.status {
			t.Errorf("%s: %d, want %d", tc.name, rec.Code, tc.status)
		}
	}
}

func TestTypedUserRoutes(t *testing.T) {
	s := newTestServer(t)

	for _, name := range []string{"Ana", "Bia", "Caio"} {
		if rec := call(t, s, "POST", "/api/v1/users", "application/json", `{"name":"`+name+`"}`); rec.Code != 201 {
			t.Fatalf("create %s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if rec := call(t, s, "PUT", "/api/v1/users/2", "application/json", `{"name":"Bia","active":false}`); rec.Code != 200 {
		t.Fatalf("update: %d %s", rec.Code, rec.Body)
	}

	list := func(query string) []any {
		rec := call(t, s, "GET", "/api/v1/users?"+query, "", "")
		if rec.Code != 200 {
			t.Fatalf("list %s: %d %s", query, rec.Code, rec.Body)
		}
		return data(t, rec).([]any)
	}
	if n := len(list("limit=10")); n != 3 {
		t.Errorf("all: %d", n)
	}
	if n := len(list("limit=10&active=true")); n != 2 {
		t.Errorf("active: %d", n)
	}
	if got := list("limit=2&page=2"); len(got) != 1 || got[0].(map[string]any)["name"] != "Caio" {
		t.Errorf("page 2: %v", got)
	}
	if rec := call(t, s, "GET", "/api/v1/users", "", ""); rec.Code != 400 {
		t.Errorf("missing limit: %d", rec.Code)
	}
	if rec := call(t, s, "GET", "/api/v1/users?limit=500", "", ""); rec.Code != 422 {
		t.Errorf("limit out of range: %d", rec.Code)
	}

	if rec := call(t, s, "DELETE", "/api/v1/users/1", "", ""); rec.Code != 204 || rec.Body.Len() != 0 {
		t.Errorf("delete: %d %q", rec.Code, rec.Body)
	}
	if rec := call(t, s, "GET", "/api/v1/users/1", "", ""); rec.Code != 404 {
		t.Errorf("deleted user: %d", rec.Code)
	}

	// the XML route answers in XML although the handler calls SendJSON
	rec := call(t, s, "POST", "/api/v1/xml/users", "application/xml", `<user><name>Duda</name></user>`)
	if rec.Code != 201 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/xml") ||
		!strings.Contains(rec.Body.String(), "<name>Duda</name>") {
		t.Fatalf("xml route: %d %s", rec.Code, rec.Body)
	}
}

func TestDocsAndProbes(t *testing.T) {
	s := newTestServer(t)
	for _, path := range []string{"/docs", "/docs/swagger.json", "/docs/swagger.yaml", "/health", "/ready"} {
		if rec := call(t, s, "GET", path, "", ""); rec.Code != 200 {
			t.Errorf("%s: %d", path, rec.Code)
		}
	}
	spec := call(t, s, "GET", "/docs/swagger.json", "", "").Body.String()
	for _, path := range []string{`"/api/v1/users/{id}"`, `"/api/v1/xml/users"`, `"/echo/json"`} {
		if !strings.Contains(spec, path) {
			t.Errorf("spec misses %s", path)
		}
	}
}

func TestSecurityAndCORS(t *testing.T) {
	s := newTestServer(t)

	rec := call(t, s, "GET", "/health", "", "")
	if rec.Header().Get("X-Frame-Options") != "SAMEORIGIN" || !strings.Contains(rec.Header().Get("Content-Security-Policy"), "default-src 'self'") {
		t.Errorf("security headers: %v", rec.Header())
	}
	// The docs page replaces the API policy with its own (nonce + jsDelivr).
	if csp := call(t, s, "GET", "/docs", "", "").Header().Get("Content-Security-Policy"); !strings.Contains(csp, "'nonce-") {
		t.Errorf("docs CSP: %q", csp)
	}

	req := httptest.NewRequest("OPTIONS", "/api/v1/users", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	req.Header.Set("Access-Control-Request-Method", "POST")
	pre := httptest.NewRecorder()
	s.Srv.Handler.ServeHTTP(pre, req)
	if pre.Code != 204 || pre.Header().Get("Access-Control-Allow-Origin") != "http://localhost:3000" || pre.Header().Get("Access-Control-Max-Age") != "600" {
		t.Errorf("preflight: %d %v", pre.Code, pre.Header())
	}
}

func TestLoginAndPrivateRoute(t *testing.T) {
	s := newTestServer(t)

	for _, body := range []string{
		`{"email":"admin@admin.com","password":"wrong"}`,
		`{"email":"other@admin.com","password":"admin123"}`,
	} {
		if rec := call(t, s, "POST", "/auth/login", "application/json", body); rec.Code != 401 {
			t.Fatalf("bad credentials %s: %d %s", body, rec.Code, rec.Body)
		}
	}
	if rec := call(t, s, "POST", "/auth/login", "application/json", `{"email":"admin@admin.com"}`); rec.Code != 400 {
		t.Fatalf("missing password: %d", rec.Code)
	}

	rec := call(t, s, "POST", "/auth/login", "application/json", `{"email":"admin@admin.com","password":"admin123"}`)
	if rec.Code != 200 {
		t.Fatalf("login: %d %s", rec.Code, rec.Body)
	}
	login := data(t, rec).(map[string]any)
	token, _ := login["access_token"].(string)
	if token == "" || login["token_type"] != "Bearer" || login["expires_in"] != float64(900) {
		t.Fatalf("login response: %v", login)
	}

	me := func(authorization string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/auth/me", nil)
		if authorization != "" {
			req.Header.Set("Authorization", authorization)
		}
		rec := httptest.NewRecorder()
		s.Srv.Handler.ServeHTTP(rec, req)
		return rec
	}
	if rec := me(""); rec.Code != 401 || !strings.HasPrefix(rec.Header().Get("WWW-Authenticate"), "Bearer") {
		t.Fatalf("no token: %d %q", rec.Code, rec.Header().Get("WWW-Authenticate"))
	}
	if rec := me("Bearer " + token + "x"); rec.Code != 401 {
		t.Fatalf("tampered token: %d", rec.Code)
	}
	rec = me("Bearer " + token)
	if rec.Code != 200 || data(t, rec).(map[string]any)["sub"] != "1" {
		t.Fatalf("me: %d %s", rec.Code, rec.Body)
	}
}

func TestCacheRoutes(t *testing.T) {
	s := newTestServer(t)

	if rec := call(t, s, "GET", "/cache/greeting", "", ""); rec.Code != 404 {
		t.Fatalf("miss: %d", rec.Code)
	}
	if rec := call(t, s, "PUT", "/cache/greeting", "application/json", `{"value":"hello","ttl":"1m"}`); rec.Code != 204 {
		t.Fatalf("put: %d %s", rec.Code, rec.Body)
	}
	if rec := call(t, s, "PUT", "/cache/greeting", "application/json", `{"value":"hello","ttl":"soon"}`); rec.Code != 400 {
		t.Fatalf("bad ttl: %d", rec.Code)
	}
	if rec := call(t, s, "PUT", "/cache/greeting", "application/json", `{"ttl":"1m"}`); rec.Code != 400 {
		t.Fatalf("missing value: %d", rec.Code)
	}
	rec := call(t, s, "GET", "/cache/greeting", "", "")
	if rec.Code != 200 || data(t, rec).(map[string]any)["value"] != "hello" {
		t.Fatalf("hit: %d %s", rec.Code, rec.Body)
	}
	if rec := call(t, s, "DELETE", "/cache/greeting", "", ""); rec.Code != 204 {
		t.Fatalf("delete: %d", rec.Code)
	}
	if rec := call(t, s, "GET", "/cache/greeting", "", ""); rec.Code != 404 {
		t.Fatalf("after delete: %d", rec.Code)
	}

	// readiness reports the cache; the database is off, so it is omitted
	ready := data(t, call(t, s, "GET", "/ready", "", "")).(map[string]any)
	if ready["cache"].(map[string]any)["health"] != true || ready["database"] != nil {
		t.Fatalf("ready: %v", ready)
	}
	if rec := call(t, s, "GET", "/db/notes", "", ""); rec.Code != 404 {
		t.Fatalf("database routes registered while disabled: %d", rec.Code)
	}
}
