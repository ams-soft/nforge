package nhttp

import (
	"encoding/json"
	"errors"
	"fmt"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	router "github.com/ams-soft/go-ams-router"
	"github.com/ams-soft/nforge/config"
	"github.com/ams-soft/nforge/database"
)

const (
	tenantOne = "3f2b8c1e-9d4a-4e6b-8a7c-1b2c3d4e5f60"
	tenantTwo = "7c9e6679-7425-40de-944b-e07fc1f90ae7"
)

// tenantHandler mounts the middlewares on GET /x and echoes the tenant.
func tenantHandler(called *bool, mws ...func(stdhttp.Handler) stdhttp.Handler) stdhttp.Handler {
	r := router.NewRouter()
	r.With(mws...).Get("/x", func(w stdhttp.ResponseWriter, req *stdhttp.Request) {
		*called = true
		id, _ := TenantID(req)
		_ = SendJSON(w, req, 200, map[string]string{"tenant": id})
	})
	return r
}

func tenantOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var data map[string]string
	if err := json.Unmarshal(decodeEnvelope(t, rec).Data, &data); err != nil {
		t.Fatal(err)
	}
	return data["tenant"]
}

func TestTenantFromClaim(t *testing.T) {
	auth := Authenticate(sharedJOSE(), "access")
	var called bool
	h := tenantHandler(&called, auth, Tenant())
	bearer := func(tenant string) string {
		return "Bearer " + issue(t, "access", "", claimsFields{TenantID: tenant})
	}

	rec := serve(h, "Authorization", bearer(strings.ToUpper(tenantOne)))
	if rec.Code != 200 || tenantOf(t, rec) != tenantOne {
		t.Fatalf("valid claim: %d %s", rec.Code, rec.Body.String())
	}

	called = false
	cases := []struct {
		name, tenant string
		status       int
		detail       string
	}{
		{"missing claim", "", 403, detailTenantRequired},
		{"not a uuid", "acme", 400, detailTenantInvalid},
		{"uuid v1", "6ba7b810-9dad-11d1-80b4-00c04fd430c8", 400, detailTenantInvalid},
	}
	for _, c := range cases {
		rec := serve(h, "Authorization", bearer(c.tenant))
		if rec.Code != c.status || decodeEnvelope(t, rec).Error.Detail != c.detail {
			t.Errorf("%s: %d %s", c.name, rec.Code, rec.Body.String())
		}
		if rec.Header().Get("WWW-Authenticate") != "" {
			t.Errorf("%s: tenant failures must not send a challenge", c.name)
		}
	}
	// Tenant without Authenticate before it
	rec = serve(tenantHandler(&called, Tenant()))
	if rec.Code != 401 || !strings.HasPrefix(rec.Header().Get("WWW-Authenticate"), "Bearer") {
		t.Errorf("no principal: %d %v", rec.Code, rec.Header())
	}
	if called {
		t.Fatal("handler invoked on a tenant failure")
	}
}

func TestTenantFromHeaderAndOptions(t *testing.T) {
	var called bool
	h := tenantHandler(&called, Tenant(WithTenantResolver(TenantFromHeader("X-Tenant-ID"))))

	if rec := serve(h, "X-Tenant-ID", " "+tenantOne+" "); rec.Code != 200 || tenantOf(t, rec) != tenantOne {
		t.Fatalf("header: %d %s", rec.Code, rec.Body.String())
	}
	if rec := serve(h); rec.Code != 403 {
		t.Errorf("missing header: %d", rec.Code)
	}
	if rec := serve(h, "X-Tenant-ID", tenantOne, "X-Tenant-ID", tenantTwo); rec.Code != 400 {
		t.Errorf("repeated header: %d", rec.Code)
	}

	optional := tenantHandler(&called, Tenant(WithTenantResolver(TenantFromHeader("X-Tenant-ID")), WithTenantOptional()))
	if rec := serve(optional); rec.Code != 200 || tenantOf(t, rec) != "" {
		t.Errorf("optional without tenant: %d %s", rec.Code, rec.Body.String())
	}
	if rec := serve(optional, "X-Tenant-ID", "bad"); rec.Code != 400 {
		t.Errorf("optional with invalid tenant: %d", rec.Code)
	}

	failing := tenantHandler(&called, Tenant(WithTenantResolver(func(*stdhttp.Request) (string, error) {
		return "", errors.New("directory down")
	})))
	if rec := serve(failing); rec.Code != 500 || strings.Contains(rec.Body.String(), "directory") {
		t.Errorf("resolver error: %d %s", rec.Code, rec.Body.String())
	}
	none := tenantHandler(&called, Tenant(WithTenantResolver(func(*stdhttp.Request) (string, error) {
		return "", database.ErrNoTenant
	})))
	if rec := serve(none); rec.Code != 403 {
		t.Errorf("resolver ErrNoTenant: %d", rec.Code)
	}
}

func TestTenantKeepsAndSetTenantOverrides(t *testing.T) {
	fixed := func(id string) TenantResolver {
		return func(*stdhttp.Request) (string, error) { return id, nil }
	}
	var called bool

	// Tenant keeps what an earlier middleware stored
	h := tenantHandler(&called, SetTenant(fixed(tenantOne)), Tenant(WithTenantResolver(fixed(tenantTwo))))
	if rec := serve(h); tenantOf(t, rec) != tenantOne {
		t.Errorf("Tenant replaced an existing tenant: %s", rec.Body.String())
	}
	// SetTenant always replaces it
	h = tenantHandler(&called, SetTenant(fixed(tenantOne)), SetTenant(fixed(tenantTwo)))
	if rec := serve(h); tenantOf(t, rec) != tenantTwo {
		t.Errorf("SetTenant kept the old tenant: %s", rec.Body.String())
	}
	if rec := serve(tenantHandler(&called, SetTenant(fixed("")))); rec.Code != 403 {
		t.Errorf("SetTenant without tenant: %d", rec.Code)
	}

	req := httptest.NewRequest("GET", "/", nil)
	if _, err := WithTenantID(req, "bad"); !errors.Is(err, database.ErrInvalidTenantID) {
		t.Errorf("WithTenantID invalid: %v", err)
	}
	req, err := WithTenantID(req, tenantTwo)
	if id, ok := TenantID(req); err != nil || !ok || id != tenantTwo {
		t.Errorf("WithTenantID = %q, %v, %v", id, ok, err)
	}
}

func TestTenantPanics(t *testing.T) {
	cases := map[string]func(){
		"header name":   func() { TenantFromHeader("X Tenant") },
		"nil resolver":  func() { WithTenantResolver(nil) },
		"nil option":    func() { Tenant(nil) },
		"nil SetTenant": func() { SetTenant(nil) },
	}
	for name, fn := range cases {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: expected a panic", name)
				}
			}()
			fn()
		}()
	}
}

func TestTypedTenantRoutes(t *testing.T) {
	s, tr := newTypedServer()
	calls := 0
	h := func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		calls++
		id, _ := TenantID(r)
		_ = SendJSON(w, r, 200, wallet{ID: id})
	}
	tr.TPost("/claim", wallet{}, wallet{}, h, Opts{IsPrivate: true, Tenant: true})
	tr.TPost("/claim-xml", wallet{}, wallet{}, h, Opts{IsPrivate: true, Tenant: true, Codec: "xml"})

	ok := issue(t, "access", "", claimsFields{TenantID: tenantOne})
	req := httptest.NewRequest("POST", "/claim", strings.NewReader(`{"id":"x"}`))
	req.Header.Set("Authorization", "Bearer "+ok)
	rec := httptest.NewRecorder()
	s.Srv.Handler.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), tenantOne) {
		t.Fatalf("claim route: %d %s", rec.Code, rec.Body.String())
	}

	// the tenant check runs before decoding and answers in the route codec
	noTenant := issue(t, "access", "", claimsFields{})
	req = httptest.NewRequest("POST", "/claim-xml", strings.NewReader("<bad"))
	req.Header.Set("Authorization", "Bearer "+noTenant)
	rec = httptest.NewRecorder()
	s.Srv.Handler.ServeHTTP(rec, req)
	if rec.Code != 403 || !strings.Contains(rec.Body.String(), "<detail>tenant required</detail>") {
		t.Fatalf("missing tenant: %d %s", rec.Code, rec.Body.String())
	}
	if calls != 1 {
		t.Fatalf("handler calls = %d, want 1", calls)
	}

	// header source works on public routes and is documented
	s, tr = newTypedServer()
	s.Deps.Config.HTTP.Tenant = config.TenantConfig{Source: "header", Header: "X-Org"}
	tr.TGet("/header", nil, wallet{}, h, Opts{Tenant: true})
	req = httptest.NewRequest("GET", "/header", nil)
	req.Header.Set("X-Org", tenantTwo)
	rec = httptest.NewRecorder()
	s.Srv.Handler.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), tenantTwo) {
		t.Fatalf("header route: %d %s", rec.Code, rec.Body.String())
	}
	if rt := s.TypedRoutes[0]; !rt.Tenant || rt.TenantHeader != "X-Org" {
		t.Fatalf("route record = %+v", rt)
	}

	data, _ := buildOpenAPI(config.DocsConfig{Auth: "bearer", Title: "t", Version: "1"}, s.TypedRoutes).JSON()
	var spec map[string]any
	_ = json.Unmarshal(data, &spec)
	op := dig(spec, "paths", "/header", "get").(map[string]any)
	if dig(op, "parameters", "0", "name") != "X-Org" || dig(op, "parameters", "0", "in") != "header" {
		t.Errorf("tenant header not documented: %v", op["parameters"])
	}
	for _, code := range []string{"400", "403"} {
		if dig(op, "responses", code) == nil {
			t.Errorf("tenant route missing %s", code)
		}
	}
}

func TestTypedTenantClaimRequiresPrivate(t *testing.T) {
	_, tr := newTypedServer()
	defer func() {
		if r := recover(); r == nil || !strings.Contains(fmt.Sprint(r), "requires IsPrivate") {
			t.Fatalf("expected a registration panic, got %v", r)
		}
	}()
	tr.TGet("/x", nil, nil, func(stdhttp.ResponseWriter, *stdhttp.Request) {}, Opts{Tenant: true})
}

func TestServerTenantMiddlewareUsesConfig(t *testing.T) {
	s, _ := newTypedServer()
	s.Deps.Config.HTTP.Tenant = config.TenantConfig{Source: "header", Header: "X-Org"}
	var called bool
	h := tenantHandler(&called, s.TenantMiddleware())
	if rec := serve(h, "X-Org", tenantOne); rec.Code != 200 || tenantOf(t, rec) != tenantOne {
		t.Fatalf("configured header: %d %s", rec.Code, rec.Body.String())
	}
	if rec := serve(h, "X-Tenant-ID", tenantOne); rec.Code != 403 {
		t.Errorf("other header must be ignored: %d", rec.Code)
	}
}
