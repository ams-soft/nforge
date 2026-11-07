package nhttp

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	router "github.com/ams-soft/go-ams-router"
)

type handlerItem struct {
	Name string `json:"name" codec:"required"`
}

type handlerQuery struct {
	Limit int `query:"limit" default:"2"`
}

func (i *handlerItem) Validate() error {
	if i.Name == "invalid" {
		return errors.New("name is invalid")
	}
	return nil
}

func handlerServer(t *testing.T, logs *bytes.Buffer) *Server {
	t.Helper()
	s := NewHTTPServer(&testDeps{Logger: slog.New(slog.NewTextHandler(logs, nil))})
	api := s.Typed()
	Get(api, "/items", func(_ *stdhttp.Request, q handlerQuery) ([]handlerItem, error) {
		return make([]handlerItem, q.Limit), nil
	})
	Get(api, "/items/{id}", func(r *stdhttp.Request, _ Empty) (handlerItem, error) {
		switch id := router.URLParam(r, "id"); id {
		case "missing":
			return handlerItem{}, Error(stdhttp.StatusNotFound, "item not found")
		case "boom":
			return handlerItem{}, errors.New("dial tcp 10.0.0.9: secret detail")
		case "bad-status":
			return handlerItem{}, Error(stdhttp.StatusOK, "not an error status")
		case "invalid":
			return handlerItem{Name: "invalid"}, nil
		default:
			return handlerItem{Name: id}, nil
		}
	})
	Post(api, "/items", func(_ *stdhttp.Request, in handlerItem) (handlerItem, error) {
		return in, nil
	}, Opts{Status: stdhttp.StatusCreated})
	Put(api, "/items/{id}", func(_ *stdhttp.Request, in handlerItem) (Empty, error) { return Empty{}, nil })
	Patch(api, "/items/{id}", func(_ *stdhttp.Request, in handlerItem) (handlerItem, error) { return in, nil })
	Delete(api, "/items/{id}", func(*stdhttp.Request, Empty) (Empty, error) { return Empty{}, nil },
		Opts{Status: stdhttp.StatusAccepted})
	return s
}

func handlerCall(s *Server, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	s.Srv.Handler.ServeHTTP(rec, req)
	return rec
}

func TestGenericHandlers(t *testing.T) {
	var logs bytes.Buffer
	s := handlerServer(t, &logs)
	tests := []struct {
		name, method, path, body string
		status                   int
		contains                 string
	}{
		{"query default", "GET", "/items", "", 200, `[{"name":""},{"name":""}]`},
		{"found", "GET", "/items/a", "", 200, `"name":"a"`},
		{"status error", "GET", "/items/missing", "", 404, "item not found"},
		{"unknown error", "GET", "/items/boom", "", 500, "internal error"},
		{"error status below 400", "GET", "/items/bad-status", "", 500, "internal error"},
		{"response fails validation", "GET", "/items/invalid", "", 500, "failed to build the response"},
		{"created", "POST", "/items", `{"name":"a"}`, 201, `"name":"a"`},
		{"missing field", "POST", "/items", `{}`, 400, "name"},
		{"validation", "POST", "/items", `{"name":"invalid"}`, 422, "name is invalid"},
		{"empty response", "PUT", "/items/1", `{"name":"a"}`, 204, ""},
		{"patch", "PATCH", "/items/1", `{"name":"a"}`, 200, `"name":"a"`},
		{"empty with status", "DELETE", "/items/1", "", 202, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := handlerCall(s, tt.method, tt.path, tt.body)
			if rec.Code != tt.status || !strings.Contains(rec.Body.String(), tt.contains) {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
		})
	}
	if out := handlerCall(s, "GET", "/items/boom", "").Body.String(); strings.Contains(out, "10.0.0.9") {
		t.Fatalf("the error leaked to the client: %s", out)
	}
	if !strings.Contains(logs.String(), "secret detail") {
		t.Fatal("the real error must reach the log")
	}
}

func TestGenericHandlersAreDocumented(t *testing.T) {
	var logs bytes.Buffer
	s := handlerServer(t, &logs)
	for _, r := range s.TypedRoutes {
		if r.Path == "/items" && r.Method == stdhttp.MethodPost && (r.Schema == nil || r.Response == nil) {
			t.Fatalf("route = %+v", r)
		}
		if r.Path == "/items/{id}" && r.Method == stdhttp.MethodDelete && (r.Schema != nil || r.Response != nil) {
			t.Fatalf("Empty must mean no schema: %+v", r)
		}
	}
}

func TestGenericHandlerRejectsANonSuccessStatus(t *testing.T) {
	defer func() {
		if r, _ := recover().(string); !strings.Contains(r, "Opts.Status") {
			t.Fatalf("panic = %q", r)
		}
	}()
	s := NewHTTPServer(&testDeps{})
	Post(s.Typed(), "/x", func(*stdhttp.Request, Empty) (Empty, error) { return Empty{}, nil }, Opts{Status: 404})
}
