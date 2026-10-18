package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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

// newTestServer loads the real config.yaml with ephemeral secrets and
// returns the server with every route.
func newTestServer(t *testing.T) *nhttp.ServerHttp {
	t.Helper()
	random := func(n int) string {
		b := make([]byte, n)
		_, _ = rand.Read(b)
		return hex.EncodeToString(b)
	}
	t.Setenv("JWT_SECRET", random(32))            // 64 chars
	t.Setenv("JWT_ENCRYPTION_SECRET", random(16)) // exactly 32 chars

	env := filepath.Join(t.TempDir(), "test.env")
	if err := os.WriteFile(env, []byte("LOGGING_OUTPUT_TERMINAL_ENABLED=false\nHTTP_SERVER_HEADER=false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFlags("config.yaml", env)
	if err != nil {
		t.Fatal(err)
	}
	dps := deps.NewDeps(*cfg)
	t.Cleanup(func() { _ = dps.Shutdown(context.Background()) })
	return newServer(dps)
}

type result struct {
	status int
	body   map[string]any
	header string
}

func call(t *testing.T, s *nhttp.ServerHttp, method, path, token, body string) result {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	s.Srv.Handler.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return result{rec.Code, out, rec.Header().Get("WWW-Authenticate")}
}

func login(t *testing.T, s *nhttp.ServerHttp, body string) string {
	t.Helper()
	r := call(t, s, "POST", "/auth/login", "", body)
	if r.status != 200 {
		t.Fatalf("login %s: %d %v", body, r.status, r.body)
	}
	data := r.body["data"].(map[string]any)
	if data["token_type"] != "Bearer" || data["expires_in"].(float64) <= 0 {
		t.Fatalf("login response: %v", data)
	}
	return data["access_token"].(string)
}

func TestAuthentication(t *testing.T) {
	s := newTestServer(t)

	if r := call(t, s, "POST", "/auth/login", "", `{"username":"admin","password":"wrong"}`); r.status != 401 {
		t.Fatalf("wrong password: %d", r.status)
	}
	if r := call(t, s, "POST", "/auth/login", "", `{"username":"admin","password":"admin","scope":"admin:all"}`); r.status != 400 {
		t.Fatalf("unknown scope: %d", r.status)
	}

	full := login(t, s, `{"username":"admin","password":"admin"}`)
	readOnly := login(t, s, `{"username":"admin","password":"admin","scope":"wallet:read"}`)
	nested := login(t, s, `{"username":"admin","password":"admin","profile":"confidential"}`)
	tampered := full[:len(full)-6] + "AAAAAA"

	cases := []struct {
		name, path, token string
		status            int
		invoked           bool
	}{
		{"public", "/public", "", 200, false},
		{"me", "/me", full, 200, true},
		{"me without token", "/me", "", 401, false},
		{"raw both scopes", "/raw/wallets", full, 200, true},
		{"raw no token", "/raw/wallets", "", 401, false},
		{"raw tampered", "/raw/wallets", tampered, 401, false},
		{"raw malformed", "/raw/wallets", "not-a-token", 401, false},
		{"raw read-only", "/raw/wallets", readOnly, 403, false},
		{"slice both scopes", "/raw/wallets-slice", full, 200, true},
		{"slice read-only", "/raw/wallets-slice", readOnly, 403, false},
		{"slice no token", "/raw/wallets-slice", "", 401, false},
		{"typed read scope", "/api/v1/wallets", readOnly, 200, true},
		{"typed nested token", "/api/v1/wallets", nested, 401, false},
		{"confidential with access token", "/api/v1/confidential", full, 401, false},
		{"confidential with nested token", "/api/v1/confidential", nested, 200, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := protectedCalls.Load()
			r := call(t, s, "GET", tc.path, tc.token, "")
			if r.status != tc.status {
				t.Fatalf("status %d, want %d: %v", r.status, tc.status, r.body)
			}
			if invoked := protectedCalls.Load() != before; invoked != tc.invoked {
				t.Fatalf("handler invoked = %v, want %v", invoked, tc.invoked)
			}
		})
	}

	// the variadic and slice forms of WithScopes behave identically
	for _, token := range []string{full, readOnly, tampered, ""} {
		a := call(t, s, "GET", "/raw/wallets", token, "")
		b := call(t, s, "GET", "/raw/wallets-slice", token, "")
		if a.status != b.status || a.header != b.header {
			t.Fatalf("variadic %d %q vs slice %d %q", a.status, a.header, b.status, b.header)
		}
	}

	me := call(t, s, "GET", "/me", full, "").body["data"].(map[string]any)
	if me["sub"] != "user-1" || me["tenant_id"] != demoTenant || me["iss"] != "https://auth.jose-example.local" {
		t.Fatalf("me = %v", me)
	}
}

func TestJOSEDemo(t *testing.T) {
	s := newTestServer(t)
	token := login(t, s, `{"username":"admin","password":"admin"}`)

	r := call(t, s, "POST", "/api/v1/jose/demo", token, `{"message":"hello"}`)
	data, _ := r.body["data"].(map[string]any)
	if r.status != 200 || data["verified"] != "hello" || data["decrypted"] != "hello" ||
		strings.Count(data["jws"].(string), ".") != 2 || strings.Count(data["jwe"].(string), ".") != 4 {
		t.Fatalf("demo: %d %v", r.status, r.body)
	}

	// the JWKS is disabled in the simple (symmetric) setup
	if r := call(t, s, "GET", "/.well-known/jwks.json", "", ""); r.status != 404 {
		t.Fatalf("jwks: %d", r.status)
	}
	ready := call(t, s, "GET", "/ready", "", "")
	if j, _ := ready.body["data"].(map[string]any)["jose"].(map[string]any); ready.status != 200 || j["health"] != true {
		t.Fatalf("ready: %d %v", ready.status, ready.body)
	}
}

func TestFailsWithoutSecret(t *testing.T) {
	t.Setenv("JWT_SECRET", "")
	t.Setenv("JWT_ENCRYPTION_SECRET", "0123456789abcdef0123456789abcdef")
	if _, err := config.LoadFlags("config.yaml", ""); err == nil || !strings.Contains(err.Error(), "secret or source is required") {
		t.Fatalf("missing JWT_SECRET must stop startup: %v", err)
	}
}
