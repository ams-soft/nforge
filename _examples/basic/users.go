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

// userID reads the path parameter; a bad one is a 400.
func userID(req *stdhttp.Request) (int64, error) {
	id, err := strconv.ParseInt(router.URLParam(req, "id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, nhttp.Error(stdhttp.StatusBadRequest, "invalid id")
	}
	return id, nil
}

var errUserNotFound = nhttp.Error(stdhttp.StatusNotFound, "user not found")

// registerUserRoutes shows typed routes: the compiler checks the request and
// response types, the request is decoded and validated before the handler, the
// response is checked after it, and everything shows up in /docs.
func registerUserRoutes(api *nhttp.TypedRouter, store *userStore) {
	tag := func(description string) nhttp.Opts { return nhttp.Opts{DocTag: "users", Description: description} }
	created := func(description string) nhttp.Opts {
		o := tag(description)
		o.Status = stdhttp.StatusCreated
		return o
	}

	// curl 'localhost:8080/api/v1/users?limit=10&name=an&active=true'
	nhttp.Get(api, "/users", func(_ *stdhttp.Request, q ListQuery) ([]User, error) {
		return store.list(q), nil
	}, tag("Lists users with filters and pagination"))

	// curl localhost:8080/api/v1/users/1
	nhttp.Get(api, "/users/{id}", func(req *stdhttp.Request, _ nhttp.Empty) (User, error) {
		id, err := userID(req)
		if err != nil {
			return User{}, err
		}
		u, ok := store.get(id)
		if !ok {
			return User{}, errUserNotFound
		}
		return u, nil
	}, tag("Gets a user"))

	// curl -X POST localhost:8080/api/v1/users -H 'Content-Type: application/json' -d '{"name":"Ana","email":"ana@example.com"}'
	nhttp.Post(api, "/users", func(_ *stdhttp.Request, u User) (User, error) {
		return store.create(u), nil
	}, created("Creates a user"))

	// curl -X PUT localhost:8080/api/v1/users/1 -H 'Content-Type: application/json' -d '{"name":"Ana Maria","active":false}'
	nhttp.Put(api, "/users/{id}", func(req *stdhttp.Request, body User) (User, error) {
		id, err := userID(req)
		if err != nil {
			return User{}, err
		}
		u, ok := store.update(id, body)
		if !ok {
			return User{}, errUserNotFound
		}
		return u, nil
	}, tag("Replaces a user"))

	// curl -X DELETE localhost:8080/api/v1/users/1
	nhttp.Delete(api, "/users/{id}", func(req *stdhttp.Request, _ nhttp.Empty) (nhttp.Empty, error) {
		id, err := userID(req)
		if err != nil || !store.delete(id) {
			return nhttp.Empty{}, errUserNotFound
		}
		return nhttp.Empty{}, nil // Empty answers 204
	}, tag("Deletes a user"))

	// The route codec decides the wire format: this route reads and writes XML.
	// curl -X POST localhost:8080/api/v1/xml/users -H 'Content-Type: application/xml' -d '<user><name>Bia</name></user>'
	xmlRoute := created("Creates a user (XML route)")
	xmlRoute.Codec = "xml"
	nhttp.Post(api, "/xml/users", func(_ *stdhttp.Request, u User) (User, error) {
		return store.create(u), nil
	}, xmlRoute)
}
