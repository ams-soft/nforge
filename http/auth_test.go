package nhttp

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	stdhttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	router "github.com/ams-soft/go-ams-router"
	"github.com/ams-soft/nforge/config"
	"github.com/ams-soft/nforge/jose"
)

var (
	joseOnce sync.Once
	joseCfg  jose.Config
	joseSvc  *jose.Service
)

// testJOSEConfig writes ephemeral keys once and returns a config with an
// access (signed) profile, a confidential (nested) profile, a generic jws
// profile and a verify-only profile.
func testJOSEConfig() jose.Config {
	joseOnce.Do(func() {
		dir, err := os.MkdirTemp("", "nhttp-jose")
		if err != nil {
			panic(err)
		}
		write := func(name string, key any) string {
			der, err := x509.MarshalPKCS8PrivateKey(key)
			if err != nil {
				panic(err)
			}
			path := filepath.Join(dir, name)
			if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
				panic(err)
			}
			return path
		}
		_, sign, _ := ed25519.GenerateKey(rand.Reader)
		_, sign2, _ := ed25519.GenerateKey(rand.Reader)
		enc, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(err)
		}
		joseCfg = jose.Config{
			Enabled: true,
			Keys: []jose.KeyConfig{
				{KID: "sign", Algorithm: jose.EdDSA, Operations: []string{jose.OpSign, jose.OpVerify}, Publish: true,
					Source: jose.KeySourceConfig{Format: "pem", File: write("sign.pem", sign)}},
				{KID: "sign-2", Algorithm: jose.EdDSA, Operations: []string{jose.OpSign}, Publish: false,
					Source: jose.KeySourceConfig{Format: "pem", File: write("sign2.pem", sign2)}},
				{KID: "enc", Algorithm: jose.RSAOAEP256, Operations: []string{jose.OpEncrypt, jose.OpDecrypt},
					Source: jose.KeySourceConfig{Format: "pem", File: write("enc.pem", enc)}},
			},
			Profiles: []jose.ProfileConfig{
				{Name: "access", Kind: jose.KindJWTSigned, Typ: "at+jwt", SigningAlgorithm: jose.EdDSA, SigningKey: "sign",
					VerificationAlgorithms: []string{jose.EdDSA}, VerificationKeys: []string{"sign"},
					JWT: jose.JWTConfig{Issuer: "https://issuer.test", Audiences: []string{"api"}}},
				{Name: "confidential", Kind: jose.KindJWTNested, Typ: "secret+jwt", SigningAlgorithm: jose.EdDSA, SigningKey: "sign",
					VerificationAlgorithms: []string{jose.EdDSA}, VerificationKeys: []string{"sign"},
					EncryptionAlgorithm: jose.RSAOAEP256, EncryptionKey: "enc",
					DecryptionAlgorithms: []string{jose.RSAOAEP256}, DecryptionKeys: []string{"enc"},
					JWT: jose.JWTConfig{Issuer: "https://issuer.test", Audiences: []string{"secret-api"}}},
				{Name: "msg", Kind: jose.KindJWS, Typ: "msg+jws", SigningAlgorithm: jose.EdDSA, SigningKey: "sign",
					VerificationAlgorithms: []string{jose.EdDSA}, VerificationKeys: []string{"sign"}},
				{Name: "issuer-only", Kind: jose.KindJWTSigned, Typ: "io+jwt", SigningAlgorithm: jose.EdDSA, SigningKey: "sign-2",
					JWT: jose.JWTConfig{Issuer: "https://issuer.test", Audiences: []string{"api"}}},
			},
			JWKS: jose.JWKSConfig{Enabled: true},
		}
		joseSvc, err = jose.New(context.Background(), joseCfg)
		if err != nil {
			panic(err)
		}
	})
	return joseCfg.Clone()
}

func sharedJOSE() *jose.Service {
	testJOSEConfig()
	return joseSvc
}

type claimsFields struct {
	TenantID string   `json:"tenant_id,omitempty"`
	Roles    []string `json:"roles,omitempty"`
	Scope    string   `json:"scope,omitempty"`
}

// issue mints a token through the real service.
func issue(t *testing.T, profile, scope string, custom any) string {
	t.Helper()
	if custom == nil {
		custom = claimsFields{TenantID: "t1", Roles: []string{"operator"}, Scope: scope}
	}
	token, err := sharedJOSE().IssueJWT(context.Background(), profile, jose.RegisteredClaims{Subject: "user-1"}, custom)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func doAuth(t *testing.T, s *ServerHttp, method, target, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	s.Srv.Handler.ServeHTTP(rec, req)
	return rec
}

// rawServer mounts handler behind the given middleware on GET /x.
func rawHandler(mw func(stdhttp.Handler) stdhttp.Handler, called *bool) stdhttp.Handler {
	r := router.NewRouter()
	r.With(mw).Get("/x", func(w stdhttp.ResponseWriter, req *stdhttp.Request) {
		*called = true
		p, _ := PrincipalFromContext(req.Context())
		_ = SendJSON(w, req, 200, map[string]any{"sub": p.Subject, "tenant": p.TenantID, "scopes": p.Scopes})
	})
	return r
}

func serve(h stdhttp.Handler, headers ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", "/x", nil)
	for i := 0; i < len(headers); i += 2 {
		req.Header.Add(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeEnvelope(t *testing.T, rec *httptest.ResponseRecorder) envelopeBody {
	t.Helper()
	var env envelopeBody
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("not an envelope: %s", rec.Body.String())
	}
	return env
}

func TestAuthenticateTransport(t *testing.T) {
	valid := issue(t, "access", "wallet:read", nil)
	tampered := valid[:len(valid)-4] + "AAAA"
	cases := []struct {
		name      string
		headers   []string
		status    int
		detail    string
		challenge string
	}{
		{"missing", nil, 401, "authentication required", `Bearer realm="nforge-api"`},
		{"other scheme", []string{"Authorization", "Basic dXNlcjpwYXNz"}, 401, "authentication required", `Bearer realm="nforge-api"`},
		{"empty header", []string{"Authorization", ""}, 400, "invalid authentication request", `Bearer realm="nforge-api", error="invalid_request"`},
		{"duplicate header", []string{"Authorization", "Bearer " + valid, "Authorization", "Bearer " + valid}, 400, "invalid authentication request", `Bearer realm="nforge-api", error="invalid_request"`},
		{"scheme only", []string{"Authorization", "Bearer"}, 400, "invalid authentication request", `Bearer realm="nforge-api", error="invalid_request"`},
		{"tab separator", []string{"Authorization", "Bearer\t" + valid}, 400, "invalid authentication request", `Bearer realm="nforge-api", error="invalid_request"`},
		{"comma credentials", []string{"Authorization", "Bearer " + valid + ", Bearer x"}, 400, "invalid authentication request", `Bearer realm="nforge-api", error="invalid_request"`},
		{"trailing space", []string{"Authorization", "Bearer " + valid + " "}, 400, "invalid authentication request", `Bearer realm="nforge-api", error="invalid_request"`},
		{"extra field", []string{"Authorization", "Bearer " + valid + " extra"}, 400, "invalid authentication request", `Bearer realm="nforge-api", error="invalid_request"`},
		{"tampered", []string{"Authorization", "Bearer " + tampered}, 401, "invalid token", `Bearer realm="nforge-api", error="invalid_token"`},
		{"garbage", []string{"Authorization", "Bearer abc"}, 401, "invalid token", `Bearer realm="nforge-api", error="invalid_token"`},
		{"oversize", []string{"Authorization", "Bearer " + strings.Repeat("a", 20000)}, 401, "invalid token", `Bearer realm="nforge-api", error="invalid_token"`},
		{"nested token on access", []string{"Authorization", "Bearer " + issue(t, "confidential", "", nil)}, 401, "invalid token", `Bearer realm="nforge-api", error="invalid_token"`},
		{"generic jws on access", []string{"Authorization", "Bearer " + mustSign(t)}, 401, "invalid token", `Bearer realm="nforge-api", error="invalid_token"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			rec := serve(rawHandler(Authenticate(sharedJOSE(), "access"), &called), tc.headers...)
			if called {
				t.Fatal("handler invoked after authentication failure")
			}
			if rec.Code != tc.status || rec.Header().Get("WWW-Authenticate") != tc.challenge {
				t.Fatalf("%d %q: %s", rec.Code, rec.Header().Get("WWW-Authenticate"), rec.Body.String())
			}
			env := decodeEnvelope(t, rec)
			if env.Error == nil || env.Error.Detail != tc.detail {
				t.Fatalf("envelope = %s", rec.Body.String())
			}
			if len(tc.headers) > 1 && strings.Contains(rec.Body.String(), valid[:20]) {
				t.Fatal("token data leaked in the response")
			}
		})
	}

	for _, scheme := range []string{"Bearer", "bearer", "BEARER", "bEaReR"} {
		called := false
		rec := serve(rawHandler(Authenticate(sharedJOSE(), "access"), &called), "Authorization", scheme+"   "+valid)
		if rec.Code != 200 || !called {
			t.Fatalf("scheme %q: %d %s", scheme, rec.Code, rec.Body.String())
		}
	}
}

func mustSign(t *testing.T) string {
	t.Helper()
	token, err := sharedJOSE().Sign(context.Background(), "msg", []byte(`{"sub":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestAuthenticateScopes(t *testing.T) {
	both := issue(t, "access", "wallet:read wallet:write", nil)
	readOnly := issue(t, "access", "wallet:read", nil)
	none := issue(t, "access", "", nil)
	required := []string{"wallet:read", "wallet:write"}

	variadic := Authenticate(sharedJOSE(), "access", WithScopes("wallet:read", "wallet:write"))
	slice := Authenticate(sharedJOSE(), "access", WithScopes(required...))
	accumulated := Authenticate(sharedJOSE(), "access", WithScopes("wallet:read"), WithScopes("wallet:write", "wallet:read"))
	chained := func(next stdhttp.Handler) stdhttp.Handler {
		return Authenticate(sharedJOSE(), "access")(RequireScopes(required...)(next))
	}
	mutated := []string{"wallet:read", "wallet:write"}
	afterMutation := Authenticate(sharedJOSE(), "access", WithScopes(mutated...))
	mutated[1] = "nothing"

	for name, mw := range map[string]func(stdhttp.Handler) stdhttp.Handler{
		"variadic": variadic, "slice": slice, "accumulated": accumulated, "RequireScopes": chained, "caller mutation": afterMutation,
	} {
		t.Run(name, func(t *testing.T) {
			for token, want := range map[string]int{both: 200, readOnly: 403, none: 403} {
				called := false
				rec := serve(rawHandler(mw, &called), "Authorization", "Bearer "+token)
				if rec.Code != want || called != (want == 200) {
					t.Fatalf("status %d (called %v), want %d: %s", rec.Code, called, want, rec.Body.String())
				}
				if want == 403 {
					if got := rec.Header().Get("WWW-Authenticate"); got != `Bearer realm="nforge-api", error="insufficient_scope", scope="wallet:read wallet:write"` {
						t.Fatalf("challenge = %q", got)
					}
					if decodeEnvelope(t, rec).Error.Detail != "insufficient permissions" {
						t.Fatal(rec.Body.String())
					}
				}
			}
		})
	}

	// no scope requirement: any valid token passes
	called := false
	if rec := serve(rawHandler(Authenticate(sharedJOSE(), "access", WithScopes()), &called), "Authorization", "Bearer "+none); rec.Code != 200 {
		t.Fatalf("WithScopes() without scopes: %d", rec.Code)
	}
	// RequireScopes alone, without a principal
	called = false
	if rec := serve(rawHandler(RequireScopes("a"), &called), "Authorization", "Bearer "+both); rec.Code != 401 || called {
		t.Fatalf("RequireScopes without Authenticate: %d", rec.Code)
	}
}

func TestAuthenticatePanics(t *testing.T) {
	cases := map[string]func(){
		"nil service":       func() { Authenticate(nil, "access") },
		"unknown profile":   func() { Authenticate(sharedJOSE(), "nope") },
		"generic profile":   func() { Authenticate(sharedJOSE(), "msg") },
		"producer only":     func() { Authenticate(sharedJOSE(), "issuer-only") },
		"empty scope":       func() { Authenticate(sharedJOSE(), "access", WithScopes("")) },
		"scope with space":  func() { Authenticate(sharedJOSE(), "access", WithScopes("a b")) },
		"scope with quote":  func() { Authenticate(sharedJOSE(), "access", WithScopes(`a"b`)) },
		"realm with quote":  func() { Authenticate(sharedJOSE(), "access", WithRealm(`a"b`)) },
		"nil option":        func() { Authenticate(sharedJOSE(), "access", nil) },
		"bad RequireScopes": func() { RequireScopes("a\tb") },
	}
	for name, fn := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("expected a panic")
				}
			}()
			fn()
		})
	}
}

func TestPrincipal(t *testing.T) {
	token := issue(t, "access", "b a b", nil)
	var got Principal
	r := router.NewRouter()
	r.With(Authenticate(sharedJOSE(), "access", WithRealm("my realm"))).Get("/x", func(w stdhttp.ResponseWriter, req *stdhttp.Request) {
		got, _ = PrincipalFromContext(req.Context())
		got.Scopes[0] = "mutated"
		again, _ := PrincipalFromContext(req.Context())
		if again.Scopes[0] == "mutated" {
			t.Error("PrincipalFromContext must return copies")
		}
		w.WriteHeader(204)
	})
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Tenant-ID", "evil") // never used for identity
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != 204 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if got.Subject != "user-1" || got.Issuer != "https://issuer.test" || got.Audience[0] != "api" || got.TokenID == "" ||
		got.TenantID != "t1" || got.Roles[0] != "operator" || len(got.Scopes) != 2 {
		t.Fatalf("principal = %+v", got)
	}
	if rec := serve(r); rec.Header().Get("WWW-Authenticate") != `Bearer realm="my realm"` {
		t.Fatalf("realm: %q", rec.Header().Get("WWW-Authenticate"))
	}

	if _, ok := PrincipalFromContext(context.Background()); ok {
		t.Fatal("no principal expected")
	}
}

func TestPrincipalMapping(t *testing.T) {
	registeredOverride := func(_ context.Context, c jose.RegisteredClaims, _ any) (Principal, error) {
		return Principal{Subject: "admin", Issuer: "x", TokenID: "y", Scopes: []string{"s"}}, nil
	}
	cases := []struct {
		name   string
		custom any
		mapper PrincipalMapper
		status int
	}{
		{"tenant wrong type", map[string]any{"tenant_id": 42}, nil, 401},
		{"roles wrong type", map[string]any{"roles": "admin"}, nil, 401},
		{"role element wrong type", map[string]any{"roles": []any{"a", 1}}, nil, 401},
		{"scope wrong type", map[string]any{"scope": []string{"a"}}, nil, 401},
		{"scope with control", map[string]any{"scope": "a\u0001b"}, nil, 401},
		{"tab separated scopes", map[string]any{"scope": "a\tb"}, nil, 401},
		{"extra custom fields", map[string]any{"other": 1}, nil, 200},
		{"mapper rejects", map[string]any{}, func(context.Context, jose.RegisteredClaims, any) (Principal, error) {
			return Principal{}, fmt.Errorf("disabled: %w", jose.ErrClaimsRejected)
		}, 401},
		{"mapper unavailable", map[string]any{}, func(context.Context, jose.RegisteredClaims, any) (Principal, error) {
			return Principal{}, fmt.Errorf("%w: db", jose.ErrValidationUnavailable)
		}, 503},
		{"mapper unknown error", map[string]any{}, func(context.Context, jose.RegisteredClaims, any) (Principal, error) {
			return Principal{}, errors.New("boom")
		}, 500},
		{"mapper invalid role", map[string]any{}, func(context.Context, jose.RegisteredClaims, any) (Principal, error) {
			return Principal{Roles: []string{""}}, nil
		}, 401},
		{"mapper cannot change identity", map[string]any{}, registeredOverride, 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var opts []AuthOption
			if tc.mapper != nil {
				opts = append(opts, WithPrincipalMapper(tc.mapper))
			}
			called := false
			rec := serve(rawHandler(Authenticate(sharedJOSE(), "access", opts...), &called), "Authorization", "Bearer "+issue(t, "access", "", tc.custom))
			if rec.Code != tc.status || called != (tc.status == 200) {
				t.Fatalf("%d (called %v): %s", rec.Code, called, rec.Body.String())
			}
			if tc.status == 200 && strings.Contains(rec.Body.String(), "admin") {
				t.Fatalf("mapper changed the registered identity: %s", rec.Body.String())
			}
			if tc.status >= 500 && rec.Header().Get("WWW-Authenticate") != "" {
				t.Fatal("no invalid-token challenge on server-side failures")
			}
		})
	}
}

func TestAuthenticateTwiceIsAnError(t *testing.T) {
	token := issue(t, "access", "", nil)
	called := false
	twice := func(next stdhttp.Handler) stdhttp.Handler {
		return Authenticate(sharedJOSE(), "access")(Authenticate(sharedJOSE(), "access")(next))
	}
	rec := serve(rawHandler(twice, &called), "Authorization", "Bearer "+token)
	if rec.Code != 500 || called {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestValidatorUnavailableAndClosedService(t *testing.T) {
	cfg := testJOSEConfig()
	svc, err := jose.New(context.Background(), cfg, jose.WithClaimsValidator("access", func(context.Context, jose.RegisteredClaims, any) error {
		return jose.ErrValidationUnavailable
	}))
	if err != nil {
		t.Fatal(err)
	}
	token := issue(t, "access", "", nil)
	called := false
	rec := serve(rawHandler(Authenticate(svc, "access"), &called), "Authorization", "Bearer "+token)
	if rec.Code != 503 || called || decodeEnvelope(t, rec).Error.Detail != "authentication unavailable" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}

	closed, _ := jose.New(context.Background(), cfg)
	h := rawHandler(Authenticate(closed, "access"), &called)
	_ = closed.Shutdown(context.Background())
	if rec := serve(h, "Authorization", "Bearer "+token); rec.Code != 503 {
		t.Fatalf("closed service: %d", rec.Code)
	}
}

// --- typed routes ---------------------------------------------------------

type wallet struct {
	ID string `json:"id" codec:"required"`
}

func TestTypedPrivateRoutes(t *testing.T) {
	s, tr := newTypedServer()
	calls := 0
	h := func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		calls++
		p, _ := PrincipalFromContext(r.Context())
		_ = SendJSON(w, r, 200, wallet{ID: p.Subject})
	}
	tr.Route("/api", func(api *TypedRouter) {
		api.TGet("/public", nil, wallet{}, h)
		api.TGet("/wallets", nil, wallet{}, h, Opts{IsPrivate: true, RequiredScopes: []string{"wallet:read"}})
		api.TPost("/wallets", wallet{}, wallet{}, h, Opts{IsPrivate: true})
		api.TPost("/xml", wallet{}, wallet{}, h, Opts{IsPrivate: true, Codec: "xml"})
		api.TPost("/cbor", wallet{}, wallet{}, h, Opts{IsPrivate: true, Codec: "cbor"})
		api.TGet("/secret", nil, wallet{}, h, Opts{IsPrivate: true, AuthProfile: "confidential"})
	})
	read := issue(t, "access", "wallet:read", nil)

	if rec := doAuth(t, s, "GET", "/api/public", ""); rec.Code != 200 {
		t.Fatalf("public: %d", rec.Code)
	}
	if rec := doAuth(t, s, "GET", "/api/wallets", read); rec.Code != 200 || !strings.Contains(rec.Body.String(), "user-1") {
		t.Fatalf("authorized: %d %s", rec.Code, rec.Body.String())
	}
	before := calls
	if rec := doAuth(t, s, "GET", "/api/wallets", issue(t, "access", "", nil)); rec.Code != 403 {
		t.Fatalf("missing scope: %d", rec.Code)
	}
	if rec := doAuth(t, s, "GET", "/api/secret", read); rec.Code != 401 {
		t.Fatalf("access token on the confidential route: %d", rec.Code)
	}
	if rec := doAuth(t, s, "GET", "/api/secret", issue(t, "confidential", "", nil)); rec.Code != 200 {
		t.Fatalf("nested token: %d %s", rec.Code, rec.Body.String())
	}
	before++

	// authentication runs before decoding: a bad body without a token is a 401, not a 400
	req := httptest.NewRequest("POST", "/api/wallets", strings.NewReader("{bad"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.Srv.Handler.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("unauthenticated bad body: %d", rec.Code)
	}

	// auth errors use the route codec
	rec = doAuth(t, s, "POST", "/api/xml", "")
	if rec.Code != 401 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/xml") || !strings.Contains(rec.Body.String(), "<detail>authentication required</detail>") {
		t.Fatalf("xml: %d %q %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
	rec = doAuth(t, s, "POST", "/api/cbor", "bad")
	if rec.Code != 401 || rec.Header().Get("Content-Type") != "application/cbor" {
		t.Fatalf("cbor: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if calls != before {
		t.Fatalf("handler invoked on failures: %d calls, want %d", calls, before)
	}

	if rt := s.TypedRoutes[1]; rt.AuthProfile != "access" || rt.RequiredScopes[0] != "wallet:read" {
		t.Fatalf("route record = %+v", rt)
	}
}

func TestTypedPrivateRoutePanics(t *testing.T) {
	h := func(stdhttp.ResponseWriter, *stdhttp.Request) {}
	cases := map[string]func(s *ServerHttp, tr *TypedRouter){
		"profile without private": func(_ *ServerHttp, tr *TypedRouter) { tr.TGet("/x", nil, nil, h, Opts{AuthProfile: "access"}) },
		"scopes without private":  func(_ *ServerHttp, tr *TypedRouter) { tr.TGet("/x", nil, nil, h, Opts{RequiredScopes: []string{"a"}}) },
		"jose disabled": func(s *ServerHttp, tr *TypedRouter) {
			s.Deps.JOSE = nil
			tr.TGet("/x", nil, nil, h, Opts{IsPrivate: true})
		},
		"no default profile": func(s *ServerHttp, tr *TypedRouter) {
			s.Deps.Config.HTTP.Auth.DefaultProfile = ""
			tr.TGet("/x", nil, nil, h, Opts{IsPrivate: true})
		},
		"unknown profile": func(_ *ServerHttp, tr *TypedRouter) {
			tr.TGet("/x", nil, nil, h, Opts{IsPrivate: true, AuthProfile: "nope"})
		},
		"generic profile": func(_ *ServerHttp, tr *TypedRouter) {
			tr.TGet("/x", nil, nil, h, Opts{IsPrivate: true, AuthProfile: "msg"})
		},
		"producer profile": func(_ *ServerHttp, tr *TypedRouter) {
			tr.TGet("/x", nil, nil, h, Opts{IsPrivate: true, AuthProfile: "issuer-only"})
		},
		"invalid scope": func(_ *ServerHttp, tr *TypedRouter) {
			tr.TGet("/x", nil, nil, h, Opts{IsPrivate: true, RequiredScopes: []string{"a b"}})
		},
		"docs auth none": func(s *ServerHttp, tr *TypedRouter) {
			s.Deps.Config.HTTP.Docs.Enabled, s.Deps.Config.HTTP.Docs.Auth = true, "none"
			tr.TGet("/x", nil, nil, h, Opts{IsPrivate: true})
		},
	}
	for name, fn := range cases {
		t.Run(name, func(t *testing.T) {
			s, tr := newTypedServer()
			defer func() {
				if r := recover(); r == nil || !strings.HasPrefix(fmt.Sprint(r), "nhttp: GET /x: ") {
					t.Fatalf("expected a registration panic, got %v", r)
				}
			}()
			fn(s, tr)
		})
	}
}

func TestOpenAPIAuthDocumentation(t *testing.T) {
	s, tr := newTypedServer()
	h := func(stdhttp.ResponseWriter, *stdhttp.Request) {}
	tr.TGet("/wallets", nil, nil, h, Opts{IsPrivate: true, AuthProfile: "access", RequiredScopes: []string{"wallet:read"}})
	tr.TGet("/me", nil, nil, h, Opts{IsPrivate: true})
	tr.TGet("/public", nil, nil, h)
	s.AddDocRoute(TypedRoute{Method: "GET", Path: "/raw", IsPrivate: true})
	cfg := config.DocsConfig{Auth: "bearer", Title: "t", Version: "1"}
	data, _ := buildOpenAPI(cfg, s.TypedRoutes).JSON()
	var spec map[string]any
	_ = json.Unmarshal(data, &spec)

	op := dig(spec, "paths", "/wallets", "get").(map[string]any)
	for _, code := range []string{"400", "401", "403", "500"} {
		if dig(op, "responses", code) == nil {
			t.Errorf("/wallets missing %s", code)
		}
	}
	if op["x-auth-profile"] != "access" || dig(op, "x-required-scopes", "0") != "wallet:read" {
		t.Errorf("extensions: %v %v", op["x-auth-profile"], op["x-required-scopes"])
	}
	if dig(op, "security", "0", "bearerAuth") == nil {
		t.Error("bearer security missing")
	}
	me := dig(spec, "paths", "/me", "get").(map[string]any)
	if dig(me, "responses", "403") != nil || me["x-auth-profile"] != "access" {
		t.Errorf("/me: %v", me)
	}
	if sec := dig(spec, "paths", "/public", "get", "security").([]any); len(sec) != 0 {
		t.Error("public route must keep explicit empty security")
	}
	if dig(spec, "components", "securitySchemes", "bearerAuth", "bearerFormat") != "JWT" {
		t.Error("bearer scheme")
	}
	if strings.Contains(string(data), "oauth2") {
		t.Error("no OAuth scheme must be generated")
	}
}

// --- JWKS -------------------------------------------------------------------
