package nhttp

import (
	"encoding/json"
	"errors"
	"log/slog"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	router "github.com/ams-soft/go-ams-router"
	"github.com/ams-soft/nforge/codec"
	"github.com/ams-soft/nforge/deps"
)

func newTypedServer() (*ServerHttp, *TypedRouter) {
	s := &ServerHttp{
		Deps: deps.Deps{Logger: *slog.New(slog.DiscardHandler), JOSE: sharedJOSE()},
		Srv:  &stdhttp.Server{Handler: router.NewRouter()},
	}
	s.Deps.Config.HTTP.Auth.DefaultProfile = "access"
	return s, s.Typed()
}

type createUser struct {
	Name  string `json:"name" codec:"required"`
	Email string `json:"email,omitempty"`
}

type userOut struct {
	ID    int64   `json:"id"`
	Name  string  `json:"name"`
	Email *string `json:"email" codec:"required"`
}

type envelopeBody struct {
	Success bool            `json:"success"`
	Status  int             `json:"status"`
	Data    json.RawMessage `json:"data"`
	Error   *struct {
		Title    string `json:"title"`
		Detail   any    `json:"detail"`
		Instance string `json:"instance"`
	} `json:"error"`
}

func do(t *testing.T, s *ServerHttp, method, target, contentType, body string) (*httptest.ResponseRecorder, envelopeBody) {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	s.Srv.Handler.ServeHTTP(rec, req)
	var env envelopeBody
	if rec.Body.Len() > 0 && strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("body is not an envelope: %v: %s", err, rec.Body.String())
		}
	}
	return rec, env
}

func TestTPost(t *testing.T) {
	s, tr := newTypedServer()
	called := false
	tr.TPost("/users", createUser{}, userOut{}, func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		called = true
		in := Body[createUser](r)
		if p := Body[*createUser](r); p == nil || p.Name != in.Name {
			t.Errorf("Body[*T] = %+v", p)
		}
		_ = SendJSON(w, r, 201, userOut{ID: 1, Name: in.Name, Email: &in.Email})
	})

	rec, env := do(t, s, "POST", "/users", "application/json", `{"name":"Ana","email":"a@x"}`)
	if rec.Code != 201 || !env.Success || env.Status != 201 || string(env.Data) != `{"id":1,"name":"Ana","email":"a@x"}` {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}

	called = false
	rec, env = do(t, s, "POST", "/users", "application/json", `{"email":"a@x"}`)
	if rec.Code != 400 || called || env.Success || env.Error.Title != "400 ERROR BAD REQUEST" || env.Error.Instance != "/users" {
		t.Fatalf("invalid body: %d %s", rec.Code, rec.Body.String())
	}
	rec, _ = do(t, s, "POST", "/users", "application/xml", `<x/>`)
	if rec.Code != 415 || called {
		t.Fatalf("content-type: %d %s", rec.Code, rec.Body.String())
	}
}

func TestTypedResponseStatus(t *testing.T) {
	s, tr := newTypedServer()
	email := "a@x"
	tr.TPut("/error", nil, userOut{}, func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		_ = SendJSON(w, r, 409, map[string]string{"reason": "duplicate"}) // error: no validation
	})
	tr.TPut("/nostatus", nil, userOut{}, func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		_, _ = w.Write([]byte(`{"id":2,"name":"Bia","email":"b@x"}`)) // raw bytes
	})
	tr.TDelete("/nocontent", nil, userOut{}, func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		w.WriteHeader(204)
	})
	tr.TPatch("/invalid", nil, userOut{}, func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		_ = SendJSON(w, r, 200, userOut{ID: 1}) // required email is nil
	})
	tr.TPatch("/mismatch", nil, userOut{}, func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		_ = SendJSON(w, r, 200, map[string]any{"id": "x"})
	})
	tr.TPatch("/convert", nil, userOut{}, func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		_ = SendJSON(w, r, 200, map[string]any{"id": 3, "email": email})
	})
	tr.TPatch("/untyped", nil, nil, func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		_ = SendXML(w, r, 202, "anything") // route is JSON: the route's codec wins
	})

	rec, env := do(t, s, "PUT", "/error", "", "")
	if rec.Code != 409 || env.Success || env.Error.Title != "409 ERROR CONFLICT" || env.Error.Detail.(map[string]any)["reason"] != "duplicate" {
		t.Errorf("error: %d %s", rec.Code, rec.Body.String())
	}
	rec, env = do(t, s, "PUT", "/nostatus", "", "")
	if rec.Code != 200 || string(env.Data) != `{"id":2,"name":"Bia","email":"b@x"}` {
		t.Errorf("nostatus: %d %s", rec.Code, rec.Body.String())
	}
	rec, _ = do(t, s, "DELETE", "/nocontent", "", "")
	if rec.Code != 204 || rec.Body.Len() != 0 {
		t.Errorf("nocontent: %d %s", rec.Code, rec.Body.String())
	}
	for _, path := range []string{"/invalid", "/mismatch"} {
		rec, env = do(t, s, "PATCH", path, "", "")
		if rec.Code != 500 || env.Error.Title != "500 ERROR INTERNAL SERVER ERROR" {
			t.Errorf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
	}
	rec, env = do(t, s, "PATCH", "/convert", "", "")
	if rec.Code != 200 || string(env.Data) != `{"id":3,"name":"","email":"a@x"}` {
		t.Errorf("convert: %d %s", rec.Code, rec.Body.String())
	}
	rec, env = do(t, s, "PATCH", "/untyped", "", "")
	if rec.Code != 202 || string(env.Data) != `"anything"` {
		t.Errorf("untyped: %d %s", rec.Code, rec.Body.String())
	}
}

func TestTypedCodec(t *testing.T) {
	s, tr := newTypedServer()
	tr.TPost("/cbor", createUser{}, createUser{}, func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		_ = SendJSON(w, r, 200, Body[createUser](r))
	}, Opts{Codec: "cbor"})

	body, _ := codec.Default().EncodeAs("cbor", createUser{Name: "Ana"})
	rec, _ := do(t, s, "POST", "/cbor", "application/cbor", string(body))
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/cbor" {
		t.Fatalf("%d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	var resp struct {
		Success bool       `json:"success"`
		Data    createUser `json:"data"`
	}
	if err := codec.Default().DecodeAs("cbor", rec.Body.Bytes(), &resp); err != nil || !resp.Success || resp.Data.Name != "Ana" {
		t.Fatalf("%v %+v", err, resp)
	}
}

type listUsers struct {
	Name   string        `query:"name"`
	Page   int           `query:"page" default:"1"`
	Limit  int           `json:"limit" codec:"required"`
	Active *bool         `query:"active"`
	Tags   []string      `query:"tag"`
	IDs    []int64       `query:"id"`
	Since  time.Time     `query:"since"`
	TTL    time.Duration `query:"ttl"`
	Skip   string        `query:"-"`
}

func (l *listUsers) Validate() error {
	if l.Limit > 100 {
		return errors.New("limit max is 100")
	}
	return nil
}

func TestTGet(t *testing.T) {
	s, tr := newTypedServer()
	var got listUsers
	tr.TGet("/users", listUsers{}, nil, func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		got = Query[listUsers](r)
		_ = SendJSON(w, r, 200, nil)
	})

	rec, _ := do(t, s, "GET", "/users?limit=10&active=true&tag=a,b&tag=c&id=1&id=2&since=2026-01-02T03:04:05Z&ttl=1m&name=", "", "")
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	since := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if got.Limit != 10 || got.Page != 1 || got.Active == nil || !*got.Active || got.Name != "" ||
		strings.Join(got.Tags, "|") != "a|b|c" || len(got.IDs) != 2 || got.IDs[1] != 2 ||
		!got.Since.Equal(since) || got.TTL != time.Minute {
		t.Fatalf("%+v", got)
	}

	cases := map[string]int{
		"/users":                       400, // limit is required
		"/users?limit=x":               400, // type
		"/users?limit=1&active=talvez": 400,
		"/users?limit=1&id=1,x":        400,
		"/users?limit=101":             422, // Validator
		"/users?limit=1&unknown=1":     200, // permissive mode
	}
	for target, want := range cases {
		if rec, _ := do(t, s, "GET", target, "", ""); rec.Code != want {
			t.Errorf("%s: %d, expected %d: %s", target, rec.Code, want, rec.Body.String())
		}
	}

	// The router refuses routes once it has served, so strict mode gets its own server.
	s, _ = newTypedServer()
	s.Deps.Config.Codec.StrictUnknownFields = true
	tr = s.Typed()
	tr.TGet("/strict", listUsers{}, nil, func(w stdhttp.ResponseWriter, r *stdhttp.Request) {})
	if rec, _ := do(t, s, "GET", "/strict?limit=1&unknown=1", "", ""); rec.Code != 400 {
		t.Errorf("strict: %d %s", rec.Code, rec.Body.String())
	}
}

func TestTGetInvalidQueryType(t *testing.T) {
	_, tr := newTypedServer()
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic for unsupported type")
		}
	}()
	tr.TGet("/x", struct{ M map[string]string }{}, nil, func(stdhttp.ResponseWriter, *stdhttp.Request) {})
}

func TestTypedRoutesRegistry(t *testing.T) {
	s, tr := newTypedServer()
	h := func(w stdhttp.ResponseWriter, r *stdhttp.Request) { _ = SendJSON(w, r, 200, "ok") }
	tr.Route("/api/v1", func(api *TypedRouter) {
		api.TGet("/users", listUsers{}, []userOut{}, func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
			_ = SendJSON(w, r, 200, []userOut{})
		}, Opts{DocTag: "users"})
		api.Route("/admin", func(adm *TypedRouter) {
			adm.TDelete("/users/{id}", nil, nil, h, Opts{IsPrivate: true, Headers: []string{"X-Tenant"}})
		})
		api.Group(func(g *TypedRouter) {
			g.TPost("/login", &createUser{}, nil, h)
		})
	})
	s.AddDocRoute(TypedRoute{Method: "get", Path: "/health", Response: map[string]string{}})

	want := []string{"GET /api/v1/users", "DELETE /api/v1/admin/users/{id}", "POST /api/v1/login", "GET /health"}
	if len(s.TypedRoutes) != len(want) {
		t.Fatalf("%d routes: %+v", len(s.TypedRoutes), s.TypedRoutes)
	}
	for i, rt := range s.TypedRoutes {
		if got := rt.Method + " " + rt.Path; got != want[i] {
			t.Errorf("route %d: %s, expected %s", i, got, want[i])
		}
		if rt.Codec != codec.JSON {
			t.Errorf("%s: codec %q", rt.Path, rt.Codec)
		}
	}
	if _, ok := s.TypedRoutes[0].Schema.(listUsers); !ok || s.TypedRoutes[0].Tag != "users" {
		t.Errorf("schema/tag: %+v", s.TypedRoutes[0])
	}
	if _, ok := s.TypedRoutes[2].Schema.(createUser); !ok {
		t.Errorf("pointer schema should become a value: %T", s.TypedRoutes[2].Schema)
	}
	if !s.TypedRoutes[1].IsPrivate || s.TypedRoutes[1].Headers[0] != "X-Tenant" || s.TypedRoutes[1].Schema != nil {
		t.Errorf("delete: %+v", s.TypedRoutes[1])
	}

	if rec, _ := do(t, s, "GET", "/api/v1/users?limit=1", "", ""); rec.Code != 200 {
		t.Errorf("public route: %d %s", rec.Code, rec.Body.String())
	}
	if rec, _ := do(t, s, "DELETE", "/api/v1/admin/users/7", "", ""); rec.Code != 401 {
		t.Errorf("private route without token: %d %s", rec.Code, rec.Body.String())
	}
	if rec := doAuth(t, s, "DELETE", "/api/v1/admin/users/7", issue(t, "access", "", nil)); rec.Code != 200 {
		t.Errorf("private route with token: %d %s", rec.Code, rec.Body.String())
	}
}
