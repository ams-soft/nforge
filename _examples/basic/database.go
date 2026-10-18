package main

import (
	"context"
	"errors"
	"fmt"
	stdhttp "net/http"
	"strconv"
	"time"

	sq "github.com/Masterminds/squirrel"
	router "github.com/ams-soft/go-ams-router"
	nhttp "github.com/ams-soft/nforge/http"
	"github.com/jackc/pgx/v5"
)

// Note is a row of basic_notes.
type Note struct {
	ID   int64  `json:"id"`
	Text string `json:"text" codec:"required"`
}

// In a real application the schema comes from a migration tool; here it is
// created at startup just so the example runs on its own. The table name
// does not clash with the database example, which may share the same database.
const notesSchema = `
CREATE TABLE IF NOT EXISTS basic_notes (
	id   BIGSERIAL PRIMARY KEY,
	text TEXT      NOT NULL
)`

// registerDatabaseRoutes registers a small CRUD over deps.DB: db.Query
// (Squirrel) builds the SQL and db.Executor (pgx) runs it. Nothing is
// registered when the database is off (standalone.env). See the database
// example for filters, transactions and multi-tenancy.
func registerDatabaseRoutes(server *nhttp.ServerHttp) error {
	db := server.Deps.DB
	if db == nil { // database.enabled: false
		return nil
	}
	logger := &server.Deps.Logger

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := db.Executor.Exec(ctx, notesSchema); err != nil {
		return fmt.Errorf("create basic_notes table: %w", err)
	}

	// pgx.ErrNoRows becomes 404; anything else is logged and becomes a
	// generic 500, never exposing database details to the client.
	fail := func(w stdhttp.ResponseWriter, r *stdhttp.Request, err error) {
		if errors.Is(err, pgx.ErrNoRows) {
			_ = nhttp.SendJSON(w, r, stdhttp.StatusNotFound, "note not found")
			return
		}
		logger.ErrorContext(r.Context(), "database error", "error", err)
		_ = nhttp.SendJSON(w, r, stdhttp.StatusInternalServerError, "internal error")
	}

	server.Typed().Route("/db/notes", func(api *nhttp.TypedRouter) {
		// curl -X POST localhost:8080/db/notes -H 'Content-Type: application/json' -d '{"text":"hello"}'
		api.TPost("/", Note{}, Note{}, func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
			in := nhttp.Body[Note](r)
			query, args, err := db.Query.Insert("basic_notes").Columns("text").Values(in.Text).
				Suffix("RETURNING id, text").ToSql()
			if err != nil {
				fail(w, r, err)
				return
			}
			var n Note
			if err := db.Executor.QueryRow(r.Context(), query, args...).Scan(&n.ID, &n.Text); err != nil {
				fail(w, r, err)
				return
			}
			_ = nhttp.SendJSON(w, r, stdhttp.StatusCreated, n)
		}, nhttp.Opts{DocTag: "database", Description: "Creates a note"})

		// curl localhost:8080/db/notes
		api.TGet("/", nil, []Note{}, func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
			query, args, err := db.Query.Select("id", "text").From("basic_notes").OrderBy("id").Limit(50).ToSql()
			if err != nil {
				fail(w, r, err)
				return
			}
			rows, err := db.Executor.Query(r.Context(), query, args...)
			if err != nil {
				fail(w, r, err)
				return
			}
			notes, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Note, error) {
				var n Note
				return n, row.Scan(&n.ID, &n.Text)
			})
			if err != nil {
				fail(w, r, err)
				return
			}
			_ = nhttp.SendJSON(w, r, stdhttp.StatusOK, notes)
		}, nhttp.Opts{DocTag: "database", Description: "Lists the first 50 notes"})

		// curl localhost:8080/db/notes/1
		api.TGet("/{id:int}", nil, Note{}, func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
			id, ok := noteID(r)
			if !ok {
				fail(w, r, pgx.ErrNoRows)
				return
			}
			query, args, err := db.Query.Select("id", "text").From("basic_notes").Where(sq.Eq{"id": id}).ToSql()
			if err != nil {
				fail(w, r, err)
				return
			}
			var n Note
			if err := db.Executor.QueryRow(r.Context(), query, args...).Scan(&n.ID, &n.Text); err != nil {
				fail(w, r, err)
				return
			}
			_ = nhttp.SendJSON(w, r, stdhttp.StatusOK, n)
		}, nhttp.Opts{DocTag: "database", Description: "Reads a note; 404 when missing"})

		// curl -X DELETE localhost:8080/db/notes/1
		api.TDelete("/{id:int}", nil, nil, func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
			id, ok := noteID(r)
			if !ok {
				fail(w, r, pgx.ErrNoRows)
				return
			}
			query, args, err := db.Query.Delete("basic_notes").Where(sq.Eq{"id": id}).ToSql()
			if err != nil {
				fail(w, r, err)
				return
			}
			tag, err := db.Executor.Exec(r.Context(), query, args...)
			if err != nil {
				fail(w, r, err)
				return
			}
			if tag.RowsAffected() == 0 { // pgx does not treat it as an error
				fail(w, r, pgx.ErrNoRows)
				return
			}
			w.WriteHeader(stdhttp.StatusNoContent)
		}, nhttp.Opts{DocTag: "database", Description: "Deletes a note; 404 when missing"})
	})
	return nil
}

// noteID reads {id:int}. The router only guarantees digits, so an id beyond
// int64 is reported as a miss instead of reaching PostgreSQL.
func noteID(r *stdhttp.Request) (int64, bool) {
	id, err := strconv.ParseInt(router.URLParam(r, "id"), 10, 64)
	return id, err == nil
}
