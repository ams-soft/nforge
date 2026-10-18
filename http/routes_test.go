package nhttp

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"encoding/xml"
	"fmt"
	"log/slog"
	stdhttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	router "github.com/ams-soft/go-ams-router"
	"github.com/ams-soft/nforge/cache"
	"github.com/ams-soft/nforge/config"
	"github.com/ams-soft/nforge/database"
	"github.com/ams-soft/nforge/deps"
	"github.com/ams-soft/nforge/jose"
	"github.com/jackc/pgx/v5/pgxpool"
	"gopkg.in/yaml.v3"
)

func newMaintenanceServer(t *testing.T, routes config.RoutesConfig, db *database.DB) *ServerHttp {
	t.Helper()
	var cfg config.Config
	cfg.App.OnlineSince = "2026-01-02T03:04:05Z"
	cfg.HTTP.Routes = routes
	s := &ServerHttp{
		Deps: deps.Deps{Config: cfg, Logger: *slog.New(slog.DiscardHandler), DB: db},
		Srv:  &stdhttp.Server{Handler: router.NewRouter()},
	}
	s.Typed().Route("/", func(api *TypedRouter) { setRoutes(api, &s.Deps) })
	return s
}

func readinessOnly() config.RoutesConfig {
	return config.RoutesConfig{Readiness: config.ReadinessConfig{Enabled: true, Path: "/ready"}}
}

func TestReadinessWithoutDatabase(t *testing.T) {
	s := newMaintenanceServer(t, readinessOnly(), nil)

	rec, env := do(t, s, stdhttp.MethodGet, "/ready", "", "")
	if rec.Code != stdhttp.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	var data map[string]any
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatal(err)
	}
	if data["status"] != "ok" || data["online_since"] != "2026-01-02T03:04:05Z" {
		t.Errorf("data = %v", data)
	}
	if _, ok := data["database"]; ok {
		t.Errorf("database must be omitted when disabled: %v", data)
	}
}

func TestReadinessDatabaseUnavailable(t *testing.T) {
	// The pool connects lazily, so creating it on a closed port succeeds and
	// the readiness ping is what fails.
	poolCfg, err := pgxpool.ParseConfig("postgres://u:p@127.0.0.1:1/db?connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), poolCfg)
	if err != nil {
		t.Fatal(err)
	}
	db := database.New(pool)
	t.Cleanup(func() { _ = db.Shutdown(context.Background()) })

	s := newMaintenanceServer(t, readinessOnly(), db)

	rec, env := do(t, s, stdhttp.MethodGet, "/ready", "", "")
	if rec.Code != stdhttp.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", rec.Code, rec.Body)
	}
	detail, _ := env.Error.Detail.(map[string]any)
	if detail["status"] != "unavailable" {
		t.Errorf("detail = %v", env.Error.Detail)
	}
	if dbInfo, _ := detail["database"].(map[string]any); dbInfo["health"] != false {
		t.Errorf("database.health must be false: %v", detail)
	}
}

func TestHealthRouteOnlyWhenEnabled(t *testing.T) {
	s := newMaintenanceServer(t, readinessOnly(), nil)
	if rec, _ := do(t, s, stdhttp.MethodGet, "/health", "", ""); rec.Code != stdhttp.StatusNotFound {
		t.Errorf("health disabled: status = %d, want 404", rec.Code)
	}

	s = newMaintenanceServer(t, config.RoutesConfig{Health: config.HealthConfig{Enabled: true, Path: "/health"}}, nil)
	if rec, _ := do(t, s, stdhttp.MethodGet, "/health", "", ""); rec.Code != stdhttp.StatusOK {
		t.Errorf("health enabled: status = %d, want 200", rec.Code)
	}
	if rec, _ := do(t, s, stdhttp.MethodGet, "/ready", "", ""); rec.Code != stdhttp.StatusNotFound {
		t.Errorf("readiness disabled: status = %d, want 404", rec.Code)
	}
}

func newCacheReadinessServer(t *testing.T) (*ServerHttp, *cache.Cache) {
	t.Helper()
	c, err := cache.Open(context.Background(), cache.Config{Enabled: true, Driver: cache.DriverMemory},
		cache.WithNamespace("test", "app"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Shutdown(context.Background()) })
	routes := readinessOnly()
	routes.Health = config.HealthConfig{Enabled: true, Path: "/health"}
	s := newMaintenanceServer(t, routes, nil)
	s.Deps.Cache = c // the routes read &s.Deps at request time
	return s, c
}

func TestReadinessWithCache(t *testing.T) {
	s, c := newCacheReadinessServer(t)

	rec, env := do(t, s, stdhttp.MethodGet, "/ready", "", "")
	if rec.Code != stdhttp.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var data map[string]any
	_ = json.Unmarshal(env.Data, &data)
	if info, _ := data["cache"].(map[string]any); info["health"] != true {
		t.Fatalf("cache.health must be true: %v", data)
	}
	if _, ok := data["database"]; ok {
		t.Fatal("database must stay omitted")
	}

	// an unavailable cache makes the probe fail with 503
	_ = c.Shutdown(context.Background())
	rec, env = do(t, s, stdhttp.MethodGet, "/ready", "", "")
	if rec.Code != stdhttp.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	detail, _ := env.Error.Detail.(map[string]any)
	if info, _ := detail["cache"].(map[string]any); info["health"] != false || detail["status"] != "unavailable" {
		t.Fatalf("detail = %v", detail)
	}
}

func TestReadinessOmitsDisabledCache(t *testing.T) {
	s := newMaintenanceServer(t, readinessOnly(), nil)
	_, env := do(t, s, stdhttp.MethodGet, "/ready", "", "")
	var data map[string]any
	_ = json.Unmarshal(env.Data, &data)
	if _, ok := data["cache"]; ok {
		t.Fatalf("cache must be omitted when disabled: %v", data)
	}
}

func TestHealthNeverTouchesCache(t *testing.T) {
	s, c := newCacheReadinessServer(t)
	_ = c.Shutdown(context.Background())
	if rec, _ := do(t, s, stdhttp.MethodGet, "/health", "", ""); rec.Code != stdhttp.StatusOK {
		t.Fatalf("liveness must not depend on the cache: %d", rec.Code)
	}
}

type docAddress struct {
	Street string `json:"street" codec:"required"`
}

type docUser struct {
	XMLName xml.Name    `json:"-" xml:"user"`
	ID      int64       `json:"id" xml:"id,attr"`
	Name    string      `json:"name" codec:"required"`
	Address *docAddress `json:"address,omitempty"`
	Friends []docUser   `json:"friends,omitempty"`
}

func newDocsServer(t *testing.T) *ServerHttp {
	t.Helper()
	s, tr := newTypedServer()
	s.Deps.Config.HTTP.Docs = config.DocsConfig{
		Enabled: true, Path: "/docs", Title: "My API", Version: "1.2.3",
		Auth: "bearer", Hosts: []string{"http://localhost:8080"}, Accent: "#007d9cbf",
	}
	r := s.Srv.Handler.(router.Router)
	s.registerDocs(r)

	h := func(stdhttp.ResponseWriter, *stdhttp.Request) {}
	tr.Route("/api", func(api *TypedRouter) {
		api.TGet("/users", listUsers{}, []docUser{}, h, Opts{DocTag: "users", Description: "List users"})
		api.TPost("/users", docUser{}, docUser{}, h, Opts{DocTag: "users", IsPrivate: true, Headers: []string{"X-Tenant"}})
		api.TDelete("/users/{id:int}", nil, nil, h, Opts{Codec: "xml"})
		api.TGet("/teams/{team:uuid}/members/{member}", nil, nil, h)
	})
	s.AddDocRoute(TypedRoute{Method: "get", Path: "/health", Response: map[string]string{}})
	return s
}

func getJSON(t *testing.T, s *ServerHttp, target string) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Srv.Handler.ServeHTTP(rec, httptest.NewRequest("GET", target, nil))
	if rec.Code != 200 {
		t.Fatalf("%s: %d %s", target, rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s: %v", target, err)
	}
	return out
}

// dig walks map keys and slice indexes ("0").
func dig(v any, keys ...string) any {
	for _, k := range keys {
		switch c := v.(type) {
		case map[string]any:
			v = c[k]
		case []any:
			i := 0
			for _, ch := range k {
				i = i*10 + int(ch-'0')
			}
			if i >= len(c) {
				return nil
			}
			v = c[i]
		default:
			return nil
		}
	}
	return v
}

func TestOpenAPISpec(t *testing.T) {
	s := newDocsServer(t)
	spec := getJSON(t, s, "/docs/swagger.json")

	checks := []struct {
		path []string
		want any
	}{
		{[]string{"openapi"}, "3.1.0"},
		{[]string{"info", "title"}, "My API"},
		{[]string{"info", "version"}, "1.2.3"},
		{[]string{"servers", "0", "url"}, "http://localhost:8080"},
		{[]string{"tags", "0", "name"}, "users"},

		// GET: query string
		{[]string{"paths", "/api/users", "get", "summary"}, "List users"},
		{[]string{"paths", "/api/users", "get", "operationId"}, "getApiUsers"},
		{[]string{"paths", "/api/users", "get", "parameters", "1", "name"}, "page"},
		{[]string{"paths", "/api/users", "get", "parameters", "1", "schema", "default"}, 1.0},
		{[]string{"paths", "/api/users", "get", "parameters", "2", "name"}, "limit"},
		{[]string{"paths", "/api/users", "get", "parameters", "2", "required"}, true},
		{[]string{"paths", "/api/users", "get", "parameters", "4", "schema", "type"}, "array"},
		{[]string{"paths", "/api/users", "get", "parameters", "6", "schema", "format"}, "date-time"},
		{[]string{"paths", "/api/users", "get", "parameters", "7", "schema", "type"}, "string"}, // Duration in the query
		{[]string{"paths", "/api/users", "get", "responses", "200", "content", "application/json", "schema", "$ref"}, "#/components/schemas/docUserListEnvelope"},
		{[]string{"components", "schemas", "docUserListEnvelope", "properties", "data", "items", "$ref"}, "#/components/schemas/docUser"},
		{[]string{"paths", "/api/users", "get", "responses", "400", "content", "application/json", "schema", "$ref"}, "#/components/schemas/ErrorEnvelope"},

		// POST: body, header, private
		{[]string{"paths", "/api/users", "post", "requestBody", "content", "application/json", "schema", "$ref"}, "#/components/schemas/docUser"},
		{[]string{"paths", "/api/users", "post", "parameters", "0", "in"}, "header"},
		{[]string{"paths", "/api/users", "post", "parameters", "0", "name"}, "X-Tenant"},
		{[]string{"paths", "/api/users", "post", "security", "0", "bearerAuth"}, []any{}},
		{[]string{"paths", "/api/users", "post", "responses", "401", "description"}, "401 ERROR UNAUTHORIZED"},
		{[]string{"paths", "/api/users", "post", "responses", "415", "description"}, "415 ERROR UNSUPPORTED MEDIA TYPE"},
		{[]string{"paths", "/api/users", "post", "responses", "401", "content", "application/json", "example", "error", "title"}, "401 ERROR UNAUTHORIZED"},
		{[]string{"paths", "/api/users", "post", "responses", "401", "content", "application/json", "example", "status"}, 401.0},

		// DELETE: typed path param, xml codec, no schema
		{[]string{"paths", "/api/users/{id}", "delete", "parameters", "0", "name"}, "id"},
		{[]string{"paths", "/api/users/{id}", "delete", "parameters", "0", "in"}, "path"},
		{[]string{"paths", "/api/users/{id}", "delete", "parameters", "0", "schema"}, map[string]any{"type": "integer", "minimum": 0}},
		{[]string{"paths", "/api/teams/{team}/members/{member}", "get", "parameters", "0", "schema"}, map[string]any{"type": "string", "format": "uuid"}},
		{[]string{"paths", "/api/teams/{team}/members/{member}", "get", "parameters", "1", "schema"}, map[string]any{"type": "string"}},
		{[]string{"paths", "/api/users/{id}", "delete", "responses", "200", "content", "application/xml", "schema", "$ref"}, "#/components/schemas/SuccessEnvelope"},
		{[]string{"components", "schemas", "SuccessEnvelope", "xml", "name"}, "response"},
		{[]string{"components", "schemas", "SuccessEnvelope", "properties", "data"}, map[string]any{}},
		{[]string{"paths", "/api/users/{id}", "delete", "requestBody"}, nil},
		{[]string{"paths", "/api/users/{id}", "delete", "responses", "400"}, nil},

		// manual route
		{[]string{"paths", "/health", "get", "responses", "200", "content", "application/json", "schema", "$ref"}, "#/components/schemas/GetHealthEnvelope"},
		{[]string{"components", "schemas", "GetHealthEnvelope", "properties", "data", "additionalProperties", "type"}, "string"},

		// components
		{[]string{"components", "schemas", "docUser", "required", "0"}, "name"},
		{[]string{"components", "schemas", "docUser", "xml", "name"}, "user"},
		{[]string{"components", "schemas", "docUser", "properties", "id", "xml", "attribute"}, true},
		{[]string{"components", "schemas", "docUser", "properties", "address", "$ref"}, "#/components/schemas/docAddress"},
		{[]string{"components", "schemas", "docUser", "properties", "friends", "items", "$ref"}, "#/components/schemas/docUser"},
		{[]string{"components", "securitySchemes", "bearerAuth", "scheme"}, "bearer"},
	}
	for _, c := range checks {
		got := dig(spec, c.path...)
		gotJSON, _ := json.Marshal(got)
		wantJSON, _ := json.Marshal(c.want)
		if string(gotJSON) != string(wantJSON) {
			t.Errorf("%s = %s, expected %s", strings.Join(c.path, "."), gotJSON, wantJSON)
		}
	}
	if sec, ok := dig(spec, "paths", "/api/users", "get", "security").([]any); !ok || len(sec) != 0 {
		t.Errorf("public route should have empty security, got %v", sec)
	}
	if got := dig(spec, "paths", "/health", "get", "summary"); got != "GET /health" {
		t.Errorf("default summary: %v", got)
	}
}

func TestOpenAPIYAMLAndPage(t *testing.T) {
	s := newDocsServer(t)

	rec := httptest.NewRecorder()
	s.Srv.Handler.ServeHTTP(rec, httptest.NewRequest("GET", "/docs/swagger.yaml", nil))
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/yaml") {
		t.Fatalf("yaml: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	var fromYAML map[string]any
	if err := yaml.Unmarshal(rec.Body.Bytes(), &fromYAML); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(rec.Body.String(), "openapi: 3.1.0\ninfo:\n") || strings.Contains(rec.Body.String(), "{") && strings.Contains(rec.Body.String(), `"openapi"`) {
		t.Errorf("yaml should be in block style and in spec order:\n%.300s", rec.Body.String())
	}
	if fromYAML["paths"].(map[string]any)["/api/users"] == nil {
		t.Error("yaml missing /api/users")
	}

	rec = httptest.NewRecorder()
	s.Srv.Handler.ServeHTTP(rec, httptest.NewRequest("GET", "/docs", nil))
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, "<h1>My API</h1>") || !strings.Contains(body, `url: "/docs/swagger.json"`) ||
		!strings.Contains(body, "v1.2.3") || !strings.Contains(body, `id="theme-toggle"`) ||
		!strings.Contains(body, "--accent: #007d9cbf;") {
		t.Fatalf("page: %d\n%s", rec.Code, body)
	}

	// auth disabled: no securitySchemes nor security
	s.Deps.Config.HTTP.Docs.Auth = "none"
	spec := getJSON(t, s, "/docs/swagger.json")
	if dig(spec, "components", "securitySchemes") != nil || dig(spec, "paths", "/api/users", "post", "security") != nil {
		t.Error("auth none still generates security")
	}
}

func newJWKSServer(t *testing.T, svc *jose.Service) *ServerHttp {
	t.Helper()
	cfg := config.Config{}
	cfg.JOSE = testJOSEConfig()
	jose.ApplyDefaults(&cfg.JOSE)
	age := 120 * time.Second
	cfg.JOSE.JWKS.CacheMaxAge = &age
	return NewHTTPServer(deps.Deps{Config: cfg, Logger: *slog.New(slog.DiscardHandler), JOSE: svc})
}

func TestJWKSEndpoint(t *testing.T) {
	s := newJWKSServer(t, sharedJOSE())
	get := func(method string, headers ...string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/.well-known/jwks.json", nil)
		for i := 0; i < len(headers); i += 2 {
			req.Header.Set(headers[i], headers[i+1])
		}
		rec := httptest.NewRecorder()
		s.Srv.Handler.ServeHTTP(rec, req)
		return rec
	}
	rec := get("GET")
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/jwk-set+json" ||
		rec.Header().Get("Cache-Control") != "public, max-age=120" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
	var set struct {
		Keys    []map[string]any `json:"keys"`
		Success *bool            `json:"success"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &set); err != nil || set.Success != nil {
		t.Fatalf("must be a raw jwk set without envelope: %s", rec.Body.String())
	}
	if len(set.Keys) != 1 || set.Keys[0]["kid"] != "sign" || set.Keys[0]["d"] != nil {
		t.Fatalf("keys = %v", set.Keys)
	}
	etag := rec.Header().Get("ETag")
	if etag == "" || get("GET").Header().Get("ETag") != etag {
		t.Fatal("ETag must be stable")
	}
	if rec := get("GET", "If-None-Match", etag); rec.Code != 304 || rec.Body.Len() != 0 {
		t.Fatalf("conditional GET: %d", rec.Code)
	}
	if rec := get("GET", "If-None-Match", `W/`+etag+`, "x"`); rec.Code != 304 {
		t.Fatalf("weak list match: %d", rec.Code)
	}
	if rec := get("GET", "If-None-Match", `"other"`); rec.Code != 200 {
		t.Fatalf("non matching: %d", rec.Code)
	}
	head := get("HEAD")
	if head.Code != 200 || head.Body.Len() != 0 || head.Header().Get("ETag") != etag || head.Header().Get("Content-Length") == "" {
		t.Fatalf("HEAD: %d %v", head.Code, head.Header())
	}

	closed, _ := jose.New(context.Background(), testJOSEConfig())
	_ = closed.Shutdown(context.Background())
	s = newJWKSServer(t, closed)
	if rec := get("GET"); rec.Code != 503 || strings.Contains(rec.Body.String(), "keys") {
		t.Fatalf("closed service: %d %s", rec.Code, rec.Body.String())
	}
}

func TestJWKSETagChangesWithKeys(t *testing.T) {
	dir := t.TempDir()
	write := func() {
		_, priv, _ := ed25519.GenerateKey(rand.Reader)
		der, _ := x509.MarshalPKCS8PrivateKey(priv)
		if err := os.WriteFile(filepath.Join(dir, "k.pem"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	jcfg := jose.Config{Enabled: true,
		Keys: []jose.KeyConfig{{KID: "k", Algorithm: jose.EdDSA, Operations: []string{jose.OpSign}, Publish: true,
			Source: jose.KeySourceConfig{Format: "pem", File: filepath.Join(dir, "k.pem")}}},
		Profiles: []jose.ProfileConfig{{Name: "m", Kind: jose.KindJWS, Typ: "m", SigningAlgorithm: jose.EdDSA, SigningKey: "k"}},
		JWKS:     jose.JWKSConfig{Enabled: true}}
	svc, err := jose.New(context.Background(), jcfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{JOSE: jcfg}
	jose.ApplyDefaults(&cfg.JOSE)
	s := NewHTTPServer(deps.Deps{Config: cfg, Logger: *slog.New(slog.DiscardHandler), JOSE: svc})
	etag := func() string {
		rec := httptest.NewRecorder()
		s.Srv.Handler.ServeHTTP(rec, httptest.NewRequest("GET", "/.well-known/jwks.json", nil))
		return rec.Header().Get("ETag")
	}
	first := etag()
	write()
	if err := svc.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if etag() == first {
		t.Fatal("ETag must change with the published keys")
	}
}

func TestJWKSPathCollision(t *testing.T) {
	cfg := config.Config{}
	cfg.JOSE = testJOSEConfig()
	jose.ApplyDefaults(&cfg.JOSE)
	cfg.JOSE.JWKS.Path = "/health"
	cfg.HTTP.Routes.Health = config.HealthConfig{Enabled: true, Path: "/health"}
	defer func() {
		if r := recover(); r == nil || !strings.Contains(fmt.Sprint(r), "collides") {
			t.Fatalf("expected a collision panic, got %v", r)
		}
	}()
	NewHTTPServer(deps.Deps{Config: cfg, Logger: *slog.New(slog.DiscardHandler), JOSE: sharedJOSE()})
}

func TestReadinessWithJOSE(t *testing.T) {
	svc, err := jose.New(context.Background(), testJOSEConfig())
	if err != nil {
		t.Fatal(err)
	}
	s := newMaintenanceServer(t, readinessOnly(), nil)
	s.Deps.JOSE = svc

	rec, env := do(t, s, "GET", "/ready", "", "")
	var data map[string]any
	_ = json.Unmarshal(env.Data, &data)
	if rec.Code != 200 || dig(data, "jose", "health") != true || data["database"] != nil {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	_ = svc.Shutdown(context.Background())
	rec, env = do(t, s, "GET", "/ready", "", "")
	detail, _ := env.Error.Detail.(map[string]any)
	if rec.Code != 503 || detail["status"] != "unavailable" || dig(detail, "jose", "health") != false {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}
