// Command basic is the smallest nForge application: configuration, logging,
// the codec (JSON, XML and CBOR from one struct), plain and typed routes,
// OpenAPI docs, health/readiness, a global middleware and minimal versions
// of the modules: a JWT login, an in-memory cache and PostgreSQL
// (standalone.env turns the database off, so no Docker is needed).
//
//	go run ./_examples/basic -config _examples/basic/config.yaml
//	go run ./_examples/basic -config _examples/basic/config.yaml -envfile _examples/basic/standalone.env
package main

import (
	"context"
	"log/slog"
	stdhttp "net/http"
	"os"

	"github.com/ams-soft/nforge/config"
	"github.com/ams-soft/nforge/deps"
	nhttp "github.com/ams-soft/nforge/http"
)

func main() {
	cfg, err := config.Load() // -config and -envfile flags; ./config.yaml by default
	if err != nil {
		slog.Error("[Fatal ERROR] unable to load config", "error", err)
		os.Exit(1)
	}

	dps := deps.NewDeps(*cfg)
	server, err := newServer(dps)
	if err != nil {
		dps.Logger.Error("[Fatal ERROR] unable to build the server", "error", err)
		_ = dps.Shutdown(context.Background())
		os.Exit(1)
	}
	if err := server.Start(); err != nil { // blocks until SIGINT/SIGTERM
		server.Shutdown()
		os.Exit(1)
	}

}

// newServer builds the server with every route; main_test.go drives it
// without listening on a port.
func newServer(dps deps.Deps) (*nhttp.ServerHttp, error) {

	// uncomment to see crypto test
	//runCrypto(&dps.Logger)

	server := nhttp.NewHTTPServer(dps)

	// Global middlewares run after the built-in ones (tracing, logger,
	// recoverer) and must be loaded before the first request.
	server.AddGlobalMiddleware(clacksOverhead)
	server.LoadGlobalMiddleware()

	registerPlainRoutes(server, &server.Deps.Logger)
	registerAuthRoutes(server)
	registerCacheRoutes(server)
	if err := registerDatabaseRoutes(server); err != nil {
		return nil, err
	}
	server.Typed().Route("/api/v1", func(api *nhttp.TypedRouter) {
		registerUserRoutes(api, newUserStore())
	})
	return server, nil
}

// clacksOverhead is a minimal global middleware: it only adds a header.
func clacksOverhead(next stdhttp.Handler) stdhttp.Handler {
	return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		w.Header().Set("X-Clacks-Overhead", "GNU Terry Pratchett")
		next.ServeHTTP(w, r)
	})
}
