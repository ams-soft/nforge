package main

// The users CRUD: db.Query (Squirrel) builds the SQL and db.Executor (pgx)
// runs it. Repository functions take *database.DB, so the same code runs on
// the pool and inside a transaction.

import (
	"context"
	"encoding/xml"
	"errors"
	"log/slog"
	stdhttp "net/http"
	"strconv"

	sq "github.com/Masterminds/squirrel"
	router "github.com/ams-soft/go-ams-router"
	"github.com/ams-soft/nforge/database"
	nhttp "github.com/ams-soft/nforge/http"
	"github.com/jackc/pgx/v5"
)

// User is used in both JSON and XML: a single struct, with the xml tag only
// where the format differs (root name and id as an attribute).
type User struct {
	XMLName xml.Name `json:"-" xml:"user"`
	ID      int64    `json:"id" xml:"id,attr"`
	Name    string   `json:"name" codec:"required"`
	Email   string   `json:"email,omitempty"`
	Active  *bool    `json:"active,omitempty" xml:"active,omitempty"` // nil on POST = database default (true)
}

// ListQuery types the query string of GET /api/v1/users.
type ListQuery struct {
	Name   string `query:"name"`
	Active *bool  `query:"active"`
	Page   int    `query:"page" default:"1"`
	Limit  int    `query:"limit" codec:"required"`
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

// In a real application the schema comes from a migration tool; here it is
// created at startup just so the example runs on its own.
const usersSchema = `
CREATE TABLE IF NOT EXISTS users (
	id         BIGSERIAL   PRIMARY KEY,
	name       TEXT        NOT NULL,
	email      TEXT        NOT NULL DEFAULT '',
	active     BOOLEAN     NOT NULL DEFAULT true,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now()
)`

// ensureUsersSchema creates the users table if needed.
func ensureUsersSchema(ctx context.Context, db *database.DB) error {
	_, err := db.Executor.Exec(ctx, usersSchema)
	return err
}

var userColumns = []string{"id", "name", "email", "active"}

// scanUser reads userColumns, in the same order.
func scanUser(row pgx.Row) (User, error) {
	var u User
	var active bool
	if err := row.Scan(&u.ID, &u.Name, &u.Email, &active); err != nil {
		return User{}, err
	}
	u.Active = &active
	return u, nil
}

// insertUser works both on the pool and inside a transaction, because it
// receives the *database.DB (which carries the right Executor).
func insertUser(ctx context.Context, db *database.DB, u User) (User, error) {
	cols, values := []string{"name", "email"}, []any{u.Name, u.Email}
	if u.Active != nil {
		cols, values = append(cols, "active"), append(values, *u.Active)
	}
	query, args, err := db.Query.
		Insert("users").
		Columns(cols...).
		Values(values...).
		Suffix("RETURNING id, name, email, active").
		ToSql()
	if err != nil {
		return User{}, err
	}
	return scanUser(db.Executor.QueryRow(ctx, query, args...))
}

// getUser reads one user; a missing id is pgx.ErrNoRows.
func getUser(ctx context.Context, db *database.DB, id int64) (User, error) {
	query, args, err := db.Query.Select(userColumns...).From("users").Where(sq.Eq{"id": id}).ToSql()
	if err != nil {
		return User{}, err
	}
	return scanUser(db.Executor.QueryRow(ctx, query, args...))
}

func userID(req *stdhttp.Request) (int64, bool) {
	id, err := strconv.ParseInt(router.URLParam(req, "id"), 10, 64)
	return id, err == nil && id > 0
}

// sendDBError translates database errors: no rows becomes 404; anything else
// is logged and becomes 500 without exposing details to the client.
func sendDBError(w stdhttp.ResponseWriter, req *stdhttp.Request, logger *slog.Logger, err error) {
	if errors.Is(err, pgx.ErrNoRows) {
		_ = nhttp.SendJSON(w, req, stdhttp.StatusNotFound, "user not found")
		return
	}
	logger.ErrorContext(req.Context(), "database error", "error", err)
	_ = nhttp.SendJSON(w, req, stdhttp.StatusInternalServerError, "internal error")
}

// registerUserRoutes registers the users CRUD under api.
func registerUserRoutes(api *nhttp.TypedRouter, db *database.DB, logger *slog.Logger) {
	// curl 'localhost:8080/api/v1/users?limit=10&active=true&name=an'
	api.TGet("/users", ListQuery{}, []User{}, func(w stdhttp.ResponseWriter, req *stdhttp.Request) {
		q := nhttp.Query[ListQuery](req)

		builder := db.Query.
			Select(userColumns...).
			From("users").
			OrderBy("id").
			Limit(uint64(q.Limit)).
			Offset(uint64((q.Page - 1) * q.Limit))
		// optional filters: each Where is ANDed, values become $N
		if q.Name != "" {
			builder = builder.Where(sq.ILike{"name": "%" + q.Name + "%"})
		}
		if q.Active != nil {
			builder = builder.Where(sq.Eq{"active": *q.Active})
		}

		query, args, err := builder.ToSql()
		if err != nil {
			sendDBError(w, req, logger, err)
			return
		}
		rows, err := db.Executor.Query(req.Context(), query, args...)
		if err != nil {
			sendDBError(w, req, logger, err)
			return
		}
		users, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (User, error) { return scanUser(row) })
		if err != nil {
			sendDBError(w, req, logger, err)
			return
		}
		_ = nhttp.SendJSON(w, req, stdhttp.StatusOK, users)
	}, nhttp.Opts{DocTag: "users", Description: "Lists users with filters and pagination"})

	// curl localhost:8080/api/v1/users/1
	api.TGet("/users/{id}", nil, User{}, func(w stdhttp.ResponseWriter, req *stdhttp.Request) {
		id, ok := userID(req)
		if !ok {
			_ = nhttp.SendJSON(w, req, stdhttp.StatusBadRequest, "invalid id")
			return
		}
		u, err := getUser(req.Context(), db, id)
		if err != nil {
			sendDBError(w, req, logger, err)
			return
		}
		_ = nhttp.SendJSON(w, req, stdhttp.StatusOK, u)
	}, nhttp.Opts{DocTag: "users", Description: "Gets a user"})

	// curl -X POST localhost:8080/api/v1/users -H 'Content-Type: application/json' --data-binary '{"name":"Ana","email":"ana@example.com"}'
	api.TPost("/users", User{}, User{}, func(w stdhttp.ResponseWriter, req *stdhttp.Request) {
		u, err := insertUser(req.Context(), db, nhttp.Body[User](req))
		if err != nil {
			sendDBError(w, req, logger, err)
			return
		}
		_ = nhttp.SendJSON(w, req, stdhttp.StatusCreated, u)
	}, nhttp.Opts{DocTag: "users", Description: "Creates a user"})

	// Transaction: either all users are created, or none.
	// curl -X POST localhost:8080/api/v1/users/batch -H 'Content-Type: application/json' --data-binary '[{"name":"Bia"},{"name":"Caio","active":false}]'
	api.TPost("/users/batch", []User{}, []User{}, func(w stdhttp.ResponseWriter, req *stdhttp.Request) {
		input := nhttp.Body[[]User](req)
		created := make([]User, 0, len(input))
		err := db.Transaction(req.Context(), func(tx *database.DB) error {
			for _, u := range input {
				// always use tx in here: the outer db would run outside the transaction
				nu, err := insertUser(req.Context(), tx, u)
				if err != nil {
					return err // roll back everything
				}
				created = append(created, nu)
			}
			return nil
		})
		if err != nil {
			sendDBError(w, req, logger, err)
			return
		}
		_ = nhttp.SendJSON(w, req, stdhttp.StatusCreated, created)
	}, nhttp.Opts{DocTag: "users", Description: "Creates several users in one transaction"})

	// curl -X PUT localhost:8080/api/v1/users/1 -H 'Content-Type: application/json' --data-binary '{"name":"Ana Maria","active":false}'
	api.TPut("/users/{id}", User{}, User{}, func(w stdhttp.ResponseWriter, req *stdhttp.Request) {
		id, ok := userID(req)
		if !ok {
			_ = nhttp.SendJSON(w, req, stdhttp.StatusBadRequest, "invalid id")
			return
		}
		u := nhttp.Body[User](req)
		builder := db.Query.
			Update("users").
			Set("name", u.Name).
			Set("email", u.Email).
			Where(sq.Eq{"id": id}).
			Suffix("RETURNING id, name, email, active")
		if u.Active != nil {
			builder = builder.Set("active", *u.Active)
		}
		query, args, err := builder.ToSql()
		if err != nil {
			sendDBError(w, req, logger, err)
			return
		}
		// RETURNING with no row = nonexistent id → pgx.ErrNoRows → 404
		updated, err := scanUser(db.Executor.QueryRow(req.Context(), query, args...))
		if err != nil {
			sendDBError(w, req, logger, err)
			return
		}
		_ = nhttp.SendJSON(w, req, stdhttp.StatusOK, updated)
	}, nhttp.Opts{DocTag: "users", Description: "Updates a user"})

	// curl -X DELETE localhost:8080/api/v1/users/1
	api.TDelete("/users/{id}", nil, nil, func(w stdhttp.ResponseWriter, req *stdhttp.Request) {
		id, ok := userID(req)
		if !ok {
			_ = nhttp.SendJSON(w, req, stdhttp.StatusBadRequest, "invalid id")
			return
		}
		query, args, err := db.Query.Delete("users").Where(sq.Eq{"id": id}).ToSql()
		if err != nil {
			sendDBError(w, req, logger, err)
			return
		}
		tag, err := db.Executor.Exec(req.Context(), query, args...)
		if err != nil {
			sendDBError(w, req, logger, err)
			return
		}
		// DELETE with no affected row is not an error in pgx: check RowsAffected
		if tag.RowsAffected() == 0 {
			sendDBError(w, req, logger, pgx.ErrNoRows)
			return
		}
		w.WriteHeader(stdhttp.StatusNoContent)
	}, nhttp.Opts{DocTag: "users", Description: "Deletes a user"})
}
