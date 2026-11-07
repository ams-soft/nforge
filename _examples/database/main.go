// Command database shows the database module: repository functions that build
// SQL with Squirrel (db.Query) and run it with pgx (db.Executor),
// transactions, error mapping, and multi-tenant scoping in column or
// Row-Level Security mode.
//
//	go run ./_examples/database -config _examples/database/config.yaml
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/ams-soft/nforge/app"
	"github.com/ams-soft/nforge/codec"
	"github.com/ams-soft/nforge/config"
	"github.com/ams-soft/nforge/database"
	nhttp "github.com/ams-soft/nforge/http"
	"github.com/ams-soft/nforge/logs"
	"github.com/ams-soft/nforge/module"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "[Fatal ERROR] unable to load config:", err)
		os.Exit(1)
	}
	application, err := newApp(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "[Fatal ERROR]", err)
		os.Exit(1)
	}
	// The database module opens and pings the pool and checks the tenancy
	// setup; a failure stops the start with nothing left open.
	if err := application.Run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "[Fatal ERROR]", err)
		os.Exit(1)
	}
}

// newApp registers the modules; the tests reuse it.
func newApp(cfg *config.Document, opts ...app.Option) (*app.Application, error) {
	mods := app.Modules(logs.Module(), codec.Module(), database.Module(), nhttp.Module(), apiModule())
	return app.New(cfg, append([]app.Option{mods}, opts...)...)
}

// apiModule prepares the schema and registers the routes during Init, before
// the HTTP listeners open.
func apiModule() module.Descriptor {
	return module.New("api", module.Definition{
		Requires: []module.Handle{nhttp.Capability, database.Capability},
		Optional: []module.Handle{module.Logger},
		Init: func(ctx context.Context, s *module.Scope) (module.Hooks, error) {
			server, db := module.Require(s, nhttp.Capability), module.Require(s, database.Capability)
			logger, ok := module.Lookup(s, module.Logger)
			if !ok {
				logger = slog.New(slog.DiscardHandler)
			}
			// A real application runs migrations; the example creates its tables
			// at startup only to be self-contained.
			ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			if err := ensureUsersSchema(ctx, db); err != nil {
				return module.Hooks{}, fmt.Errorf("create users table: %w", err)
			}
			if err := ensureNotesSchema(ctx, db); err != nil {
				return module.Hooks{}, fmt.Errorf("create notes table: %w", err)
			}
			server.Typed().Route("/api/v1", func(api *nhttp.TypedRouter) {
				registerUserRoutes(api, db, logger)
				registerNoteRoutes(api, db, logger)
			})
			registerRawNoteRoute(server, db, logger)
			return module.Hooks{}, nil
		},
	})
}
