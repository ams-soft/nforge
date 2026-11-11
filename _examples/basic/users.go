package main

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"

	router "github.com/ams-soft/go-ams-router"
	nhttp "github.com/ams-soft/nforge/http"
)

type User struct {
	ID    int64  `json:"id"`
	Name  string `json:"name" codec:"required"`
	Email string `json:"email,omitempty"`
}

func (u *User) Validate() error {
	if strings.TrimSpace(u.Name) == "" {
		return errors.New("name is required")
	}
	return nil
}

type userStore struct {
	mu     sync.RWMutex
	nextID int64
	users  map[int64]User
}

func newUserStore() *userStore { return &userStore{users: make(map[int64]User)} }

func (s *userStore) create(u User) User {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	u.ID = s.nextID
	s.users[u.ID] = u
	return u
}

func (s *userStore) list() []User {
	s.mu.RLock()
	defer s.mu.RUnlock()
	users := make([]User, 0, len(s.users))
	for _, u := range s.users {
		users = append(users, u)
	}
	return users
}

func (s *userStore) get(id int64) (User, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.users[id]
	return u, ok
}

func (s *userStore) update(id int64, u User) (User, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.users[id]; !ok {
		return User{}, false
	}
	u.ID = id
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

func listUsers(s *userStore) nhttp.Handler[nhttp.Empty, []User] {
	return func(_ *http.Request, _ nhttp.Empty) ([]User, error) { return s.list(), nil }
}

func createUser(s *userStore) nhttp.Handler[User, User] {
	return func(_ *http.Request, u User) (User, error) { return s.create(u), nil }
}

func getUser(s *userStore) nhttp.Handler[nhttp.Empty, User] {
	return func(r *http.Request, _ nhttp.Empty) (User, error) {
		id, err := parseUserID(r)
		if err != nil {
			return User{}, err
		}
		u, ok := s.get(id)
		if !ok {
			return User{}, nhttp.Error(http.StatusNotFound, "user not found")
		}
		return u, nil
	}
}

func updateUser(s *userStore) nhttp.Handler[User, User] {
	return func(r *http.Request, u User) (User, error) {
		id, err := parseUserID(r)
		if err != nil {
			return User{}, err
		}
		updated, ok := s.update(id, u)
		if !ok {
			return User{}, nhttp.Error(http.StatusNotFound, "user not found")
		}
		return updated, nil
	}
}

func deleteUser(s *userStore) nhttp.Handler[nhttp.Empty, nhttp.Empty] {
	return func(r *http.Request, _ nhttp.Empty) (nhttp.Empty, error) {
		id, err := parseUserID(r)
		if err != nil {
			return nhttp.Empty{}, err
		}
		if !s.delete(id) {
			return nhttp.Empty{}, nhttp.Error(http.StatusNotFound, "user not found")
		}
		return nhttp.Empty{}, nil
	}
}

func parseUserID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(router.URLParam(r, "id"), 10, 64)
	if err != nil || id < 1 {
		return 0, nhttp.Error(http.StatusBadRequest, "invalid user id")
	}
	return id, nil
}
