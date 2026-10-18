// Command database shows deps.DB: repository functions that build SQL with
// Squirrel (db.Query) and run it with pgx (db.Executor), transactions, error
// mapping, and multi-tenant scoping in column or Row-Level Security mode.
//
//	go run ./_examples/database -config _examples/database/config.yaml
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/ams-soft/nforge/config"
	"github.com/ams-soft/nforge/deps"
	nhttp "github.com/ams-soft/nforge/http"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("[Fatal ERROR] unable to load config", "error", err)
		os.Exit(1)
	}

	dps := deps.NewDeps(*cfg) // opens and pings the pool, checks the tenancy setup
	server, err := newServer(dps)
	if err != nil {
		dps.Logger.Error("[Fatal ERROR] unable to prepare the schema", "error", err)
		_ = dps.Shutdown(context.Background())
		os.Exit(1)
	}
	if err := server.Start(); err != nil {
		server.Shutdown()
		os.Exit(1)
	}
}

// newServer prepares the schema and registers the routes; the integration
// tests drive it without listening on a port.
func newServer(dps deps.Deps) (*nhttp.ServerHttp, error) {
	// A real application runs migrations; the example creates its tables at
	// startup only to be self-contained.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := ensureUsersSchema(ctx, dps.DB); err != nil {
		return nil, fmt.Errorf("create users table: %w", err)
	}
	if err := ensureNotesSchema(ctx, dps.DB); err != nil {
		return nil, fmt.Errorf("create notes table: %w", err)
	}

	server := nhttp.NewHTTPServer(dps)
	logger := &server.Deps.Logger
	server.Typed().Route("/api/v1", func(api *nhttp.TypedRouter) {
		registerUserRoutes(api, dps.DB, logger)
		registerNoteRoutes(api, dps.DB, logger)
	})
	registerRawNoteRoute(server, dps.DB, logger)
	return server, nil
}
