// Command jose shows nForge's JOSE support: a demo login that issues JWTs,
// raw and typed routes protected by bearer authentication and scopes,
// nested (signed + encrypted) tokens and a generic JWS/JWE round trip.
//
//	export JWT_SECRET=$(openssl rand -hex 32) JWT_ENCRYPTION_SECRET=$(openssl rand -hex 16)
//	go run ./_examples/jose -config _examples/jose/config.yaml
package main

import (
	"log/slog"
	"os"

	"github.com/ams-soft/nforge/config"
	"github.com/ams-soft/nforge/deps"
	nhttp "github.com/ams-soft/nforge/http"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		// e.g. "secret or source is required" when JWT_SECRET is unset
		slog.Error("[Fatal ERROR] unable to load config", "error", err)
		os.Exit(1)
	}

	server := newServer(deps.NewDeps(*cfg)) // deps loads and validates every key
	if err := server.Start(); err != nil {
		server.Shutdown()
		os.Exit(1)
	}
}

// newServer builds the server with every route; main_test.go drives it
// without listening on a port.
func newServer(dps deps.Deps) *nhttp.ServerHttp {
	server := nhttp.NewHTTPServer(dps)
	registerRoutes(server)
	return server
}
