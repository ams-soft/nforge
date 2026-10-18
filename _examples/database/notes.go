package main

// The multi-tenant notes: every note belongs to the tenant of the request
// that created it (the X-Tenant-ID header here, http.tenant.source: header),
// and a tenant only ever sees its own notes. It works in both
// database.tenancy modes:
//
//   - column: the queries add the tenant predicate (TenantEq) and write the
//     tenant column themselves;
//   - rls: PostgreSQL policies enforce the same rule even for a query that
//     forgets TenantEq (which these queries still add, as defense in depth).

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	stdhttp "net/http"
	"strconv"
	"strings"
	"time"

	sq "github.com/Masterminds/squirrel"
	router "github.com/ams-soft/go-ams-router"
	"github.com/ams-soft/nforge/database"
	nhttp "github.com/ams-soft/nforge/http"
	"github.com/jackc/pgx/v5"
)

// Note is a tenant-owned record.
type Note struct {
	ID        int64     `json:"id"`
	TenantID  string    `json:"tenant_id"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"created_at"`
}

// NewNote is the body of POST /api/v1/notes.
type NewNote struct {
	Text string `json:"text" codec:"required"`
}

// Validate bounds the text; a failure is a 422.
func (n *NewNote) Validate() error {
	if n.Text == "" || len(n.Text) > 1000 {
		return errors.New("text must have between 1 and 1000 bytes")
	}
	return nil
}

// ensureNotesSchema creates the notes table for the configured tenancy (column
// name, and in rls mode the forced policy on the configured setting). A real
// application does this in its migrations; the identifiers come from the
// validated tenancy config, so formatting them into the DDL is safe.
//
// The startup tenancy check runs before this, which is why the example does
// not list notes in database.tenancy.tables; with migrations, list it.
func ensureNotesSchema(ctx context.Context, db *database.DB) error {
	t := db.Tenancy()
	if !t.Enabled {
		return database.ErrTenancyDisabled
	}
	ddl := fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS notes (
			id         BIGSERIAL   PRIMARY KEY,
			%[1]s      uuid        NOT NULL,
			text       TEXT        NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		);
		CREATE INDEX IF NOT EXISTS notes_%[1]s_idx ON notes (%[1]s, id)`, t.Column)
	if _, err := db.Executor.Exec(ctx, ddl); err != nil {
		return err
	}
	if t.Mode != database.TenancyRLS {
		return nil
	}
	// Each step only runs when missing, so a role that does not own the table
	// can still start once the owner has applied it.
	predicate := fmt.Sprintf("%s = NULLIF(current_setting('%s', true), '')::uuid", t.Column, t.Setting)
	rls := fmt.Sprintf(`
		DO $$
		BEGIN
			IF NOT (SELECT relrowsecurity AND relforcerowsecurity FROM pg_class WHERE oid = 'notes'::regclass) THEN
				ALTER TABLE notes ENABLE ROW LEVEL SECURITY;
				ALTER TABLE notes FORCE ROW LEVEL SECURITY;
			END IF;
			IF NOT EXISTS (SELECT 1 FROM pg_policy WHERE polrelid = 'notes'::regclass AND polname = 'tenant_isolation') THEN
				CREATE POLICY tenant_isolation ON notes USING (%[1]s) WITH CHECK (%[1]s);
			END IF;
		END $$`, predicate)
	_, err := db.Executor.Exec(ctx, rls)
	return err
}

// noteColumns uses the configured tenant column, like the schema.
func noteColumns(db *database.DB) []string {
	return []string{"id", db.TenantColumn(), "text", "created_at"}
}

func scanNote(row pgx.Row) (Note, error) {
	var n Note
	err := row.Scan(&n.ID, &n.TenantID, &n.Text, &n.CreatedAt)
	return n, err
}

// The functions below receive a tenant-scoped DB (db.ForTenant /
// db.ForContext). TenantEq is added first and unconditionally; in rls mode the
// policy would filter anyway.

func listNotes(ctx context.Context, db *database.DB) ([]Note, error) {
	query, args, err := db.Query.Select(noteColumns(db)...).From("notes").
		Where(db.TenantEq()).
		OrderBy("id").Limit(100).ToSql()
	if err != nil {
		return nil, err
	}
	rows, err := db.Executor.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Note, error) { return scanNote(row) })
}

func getNote(ctx context.Context, db *database.DB, id int64) (Note, error) {
	query, args, err := db.Query.Select(noteColumns(db)...).From("notes").
		Where(db.TenantEq()).
		Where(sq.Eq{"id": id}). // another tenant's id → no row → 404
		ToSql()
	if err != nil {
		return Note{}, err
	}
	return scanNote(db.Executor.QueryRow(ctx, query, args...))
}

func createNote(ctx context.Context, db *database.DB, text string) (Note, error) {
	query, args, err := db.Query.Insert("notes").
		Columns(db.TenantColumn(), "text").
		Values(db.TenantID(), text). // the tenant always comes from the scope, never from the body
		Suffix("RETURNING " + strings.Join(noteColumns(db), ", ")).
		ToSql()
	if err != nil {
		return Note{}, err
	}
	return scanNote(db.Executor.QueryRow(ctx, query, args...))
}

// noteHandler adapts a function over the tenant-scoped DB to an HTTP handler.
func noteHandler(db *database.DB, logger *slog.Logger, fn func(w stdhttp.ResponseWriter, req *stdhttp.Request, tdb *database.DB) error) stdhttp.HandlerFunc {
	return func(w stdhttp.ResponseWriter, req *stdhttp.Request) {
		// the tenant middleware (Opts.Tenant or nhttp.Tenant) stored it in the context
		tdb, err := db.ForContext(req.Context())
		if err == nil {
			err = fn(w, req, tdb)
		}
		switch {
		case err == nil:
		case errors.Is(err, pgx.ErrNoRows):
			_ = nhttp.SendJSON(w, req, stdhttp.StatusNotFound, "note not found")
		default:
			logger.ErrorContext(req.Context(), "notes: database error", "error", err)
			_ = nhttp.SendJSON(w, req, stdhttp.StatusInternalServerError, "internal error")
		}
	}
}

// registerNoteRoutes registers the typed notes routes; api is the /api/v1
// router. Opts.Tenant resolves the tenant from http.tenant (the X-Tenant-ID
// header here) before decoding: missing → 403, not a UUID v4 → 400.
func registerNoteRoutes(api *nhttp.TypedRouter, db *database.DB, logger *slog.Logger) {
	tenantRoute := func(description string) nhttp.Opts {
		return nhttp.Opts{DocTag: "notes", Description: description, Tenant: true}
	}

	// curl localhost:8080/api/v1/notes -H "X-Tenant-ID: $A"
	api.TGet("/notes", nil, []Note{}, noteHandler(db, logger, func(w stdhttp.ResponseWriter, req *stdhttp.Request, tdb *database.DB) error {
		notes, err := listNotes(req.Context(), tdb)
		if err == nil {
			_ = nhttp.SendJSON(w, req, stdhttp.StatusOK, notes)
		}
		return err
	}), tenantRoute("Lists the notes of the request's tenant"))

	// curl localhost:8080/api/v1/notes/1 -H "X-Tenant-ID: $A"
	api.TGet("/notes/{id}", nil, Note{}, noteHandler(db, logger, func(w stdhttp.ResponseWriter, req *stdhttp.Request, tdb *database.DB) error {
		id, err := strconv.ParseInt(router.URLParam(req, "id"), 10, 64)
		if err != nil || id < 1 {
			_ = nhttp.SendJSON(w, req, stdhttp.StatusBadRequest, "invalid id")
			return nil //nolint:nilerr // the 400 above is the response
		}
		n, err := getNote(req.Context(), tdb, id)
		if err == nil {
			_ = nhttp.SendJSON(w, req, stdhttp.StatusOK, n)
		}
		return err
	}), tenantRoute("Gets a note of the request's tenant (404 for other tenants' notes)"))

	// curl -X POST localhost:8080/api/v1/notes -H "X-Tenant-ID: $A" -H 'Content-Type: application/json' --data-binary '{"text":"hello"}'
	api.TPost("/notes", NewNote{}, Note{}, noteHandler(db, logger, func(w stdhttp.ResponseWriter, req *stdhttp.Request, tdb *database.DB) error {
		n, err := createNote(req.Context(), tdb, nhttp.Body[NewNote](req).Text)
		if err == nil {
			_ = nhttp.SendJSON(w, req, stdhttp.StatusCreated, n)
		}
		return err
	}), tenantRoute("Creates a note for the request's tenant"))

	// Transaction on the tenant-scoped DB: in rls mode the tenant is set once after BEGIN.
	// curl -X POST localhost:8080/api/v1/notes/batch -H "X-Tenant-ID: $A" -H 'Content-Type: application/json' --data-binary '[{"text":"a"},{"text":"b"}]'
	api.TPost("/notes/batch", []NewNote{}, []Note{}, noteHandler(db, logger, func(w stdhttp.ResponseWriter, req *stdhttp.Request, tdb *database.DB) error {
		input := nhttp.Body[[]NewNote](req)
		created := make([]Note, 0, len(input))
		err := tdb.Transaction(req.Context(), func(tx *database.DB) error {
			for _, in := range input {
				n, err := createNote(req.Context(), tx, in.Text) // tx keeps the tenant
				if err != nil {
					return err
				}
				created = append(created, n)
			}
			return nil
		})
		if err == nil {
			_ = nhttp.SendJSON(w, req, stdhttp.StatusCreated, created)
		}
		return err
	}), tenantRoute("Creates several notes in one transaction"))
}

// registerRawNoteRoute serves the same listing as a raw route:
// server.TenantMiddleware() is the guard of Opts.Tenant, configured from
// http.tenant, for plain routes and groups.
func registerRawNoteRoute(server *nhttp.ServerHttp, db *database.DB, logger *slog.Logger) {
	// curl localhost:8080/raw/notes -H "X-Tenant-ID: $A"
	server.Router().With(server.TenantMiddleware()).Get("/raw/notes", noteHandler(db, logger, func(w stdhttp.ResponseWriter, req *stdhttp.Request, tdb *database.DB) error {
		notes, err := listNotes(req.Context(), tdb)
		if err == nil {
			_ = nhttp.SendJSON(w, req, stdhttp.StatusOK, notes)
		}
		return err
	}))
	server.AddDocRoute(nhttp.TypedRoute{Method: "GET", Path: "/raw/notes", Tag: "notes", Tenant: true,
		TenantHeader: server.Deps.Config.HTTP.Tenant.Header, Description: "Lists notes (raw route with TenantMiddleware)", Response: []Note{}})
}
