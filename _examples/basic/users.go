package main

import (
	"cmp"
	"encoding/xml"
	"errors"
	stdhttp "net/http"
	"slices"
	"strconv"
	"strings"
	"sync"

	router "github.com/ams-soft/go-ams-router"
	nhttp "github.com/ams-soft/nforge/http"
)

// User is one struct for JSON, XML and CBOR: cbor and xml names fall back to
// the json tag, so the xml tag only appears where XML differs.
type User struct {
	XMLName xml.Name `json:"-" xml:"user"`
	ID      int64    `json:"id" xml:"id,attr"`
	Name    string   `json:"name" codec:"required"` // required in every format
	Email   string   `json:"email,omitempty"`
	Active  *bool    `json:"active,omitempty"` // nil on create = true
}

// Validate runs after decoding (and on typed responses); a failure is a 422.
func (u *User) Validate() error {
	if strings.TrimSpace(u.Name) == "" || len(u.Name) > 100 {
		return errors.New("name must have between 1 and 100 characters")
	}
	if u.Email != "" && !strings.Contains(u.Email, "@") {
		return errors.New("email is invalid")
	}
	return nil
}

// ListQuery types the query string of GET /api/v1/users.
type ListQuery struct {
	Name   string `query:"name"`                   // optional filter
	Active *bool  `query:"active"`                 // nil when absent
	Page   int    `query:"page" default:"1"`       // default when absent
	Limit  int    `query:"limit" codec:"required"` // 400 when absent
}

// Validate checks the paging; a failure is a 422.
func (q *ListQuery) Validate() error {
	if q.Limit < 1 || q.Limit > 100 {
		return errors.New("limit must be between 1 and 100")
	}
	if q.Page < 1 {
		return errors.New("page must be greater than 0")
	}
	return nil
}

// userStore is an in-memory repository, enough for an HTTP example; the
// database example keeps users in PostgreSQL.
type userStore struct {
	mu     sync.Mutex
	nextID int64
	users  map[int64]User
}

func newUserStore() *userStore { return &userStore{users: map[int64]User{}} }

func (s *userStore) create(u User) User {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	u.ID = s.nextID
	if u.Active == nil {
		active := true
		u.Active = &active
	}
	s.users[u.ID] = u
	return u
}

func (s *userStore) get(id int64) (User, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[id]
	return u, ok
}

func (s *userStore) update(id int64, u User) (User, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.users[id]
	if !ok {
		return User{}, false
	}
	u.ID = id
	if u.Active == nil {
		u.Active = old.Active
	}
	s.users[id] = u
	return u, true
}

func (s *userStore) delete(id int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.users[id]
	delete(s.users, id)
	return ok
}

func (s *userStore) list(q ListQuery) []User {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []User{}
	for _, u := range s.users {
		if q.Name != "" && !strings.Contains(strings.ToLower(u.Name), strings.ToLower(q.Name)) {
			continue
		}
		if q.Active != nil && *u.Active != *q.Active {
			continue
		}
		out = append(out, u)
	}
	slices.SortFunc(out, func(a, b User) int { return cmp.Compare(a.ID, b.ID) })
	start := min((q.Page-1)*q.Limit, len(out))
	return out[start:min(start+q.Limit, len(out))]
}

func userID(req *stdhttp.Request) (int64, bool) {
	id, err := strconv.ParseInt(router.URLParam(req, "id"), 10, 64)
	return id, err == nil && id > 0
}

// registerUserRoutes shows typed routes: the request is decoded and
// validated before the handler, the response is checked against the
// declared type after it, and everything shows up in /docs.
func registerUserRoutes(api *nhttp.TypedRouter, store *userStore) {
	tag := func(description string) nhttp.Opts { return nhttp.Opts{DocTag: "users", Description: description} }

	// curl 'localhost:8080/api/v1/users?limit=10&name=an&active=true'
	api.TGet("/users", ListQuery{}, []User{}, func(w stdhttp.ResponseWriter, req *stdhttp.Request) {
		_ = nhttp.SendJSON(w, req, stdhttp.StatusOK, store.list(nhttp.Query[ListQuery](req)))
	}, tag("Lists users with filters and pagination"))

	// curl localhost:8080/api/v1/users/1
	api.TGet("/users/{id}", nil, User{}, func(w stdhttp.ResponseWriter, req *stdhttp.Request) {
		id, ok := userID(req)
		if !ok {
			_ = nhttp.SendJSON(w, req, stdhttp.StatusBadRequest, "invalid id")
			return
		}
		u, ok := store.get(id)
		if !ok {
			_ = nhttp.SendJSON(w, req, stdhttp.StatusNotFound, "user not found")
			return
		}
		_ = nhttp.SendJSON(w, req, stdhttp.StatusOK, u)
	}, tag("Gets a user"))

	// curl -X POST localhost:8080/api/v1/users -H 'Content-Type: application/json' -d '{"name":"Ana","email":"ana@example.com"}'
	api.TPost("/users", User{}, User{}, func(w stdhttp.ResponseWriter, req *stdhttp.Request) {
		_ = nhttp.SendJSON(w, req, stdhttp.StatusCreated, store.create(nhttp.Body[User](req)))
	}, tag("Creates a user"))

	// curl -X PUT localhost:8080/api/v1/users/1 -H 'Content-Type: application/json' -d '{"name":"Ana Maria","active":false}'
	api.TPut("/users/{id}", User{}, User{}, func(w stdhttp.ResponseWriter, req *stdhttp.Request) {
		id, ok := userID(req)
		if !ok {
			_ = nhttp.SendJSON(w, req, stdhttp.StatusBadRequest, "invalid id")
			return
		}
		u, ok := store.update(id, nhttp.Body[User](req))
		if !ok {
			_ = nhttp.SendJSON(w, req, stdhttp.StatusNotFound, "user not found")
			return
		}
		_ = nhttp.SendJSON(w, req, stdhttp.StatusOK, u)
	}, tag("Replaces a user"))

	// curl -X DELETE localhost:8080/api/v1/users/1
	api.TDelete("/users/{id}", nil, nil, func(w stdhttp.ResponseWriter, req *stdhttp.Request) {
		id, ok := userID(req)
		if !ok || !store.delete(id) {
			_ = nhttp.SendJSON(w, req, stdhttp.StatusNotFound, "user not found")
			return
		}
		w.WriteHeader(stdhttp.StatusNoContent) // no body
	}, tag("Deletes a user"))

	// The route codec decides the wire format: this route reads and writes
	// XML even though the handler calls SendJSON.
	// curl -X POST localhost:8080/api/v1/xml/users -H 'Content-Type: application/xml' -d '<user><name>Bia</name></user>'
	api.TPost("/xml/users", User{}, User{}, func(w stdhttp.ResponseWriter, req *stdhttp.Request) {
		_ = nhttp.SendJSON(w, req, stdhttp.StatusCreated, store.create(nhttp.Body[User](req)))
	}, nhttp.Opts{DocTag: "users", Description: "Creates a user (XML route)", Codec: "xml"})
}
