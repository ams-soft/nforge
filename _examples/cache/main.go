// Command cache shows deps.Cache with the same code for both drivers
// (cache.driver: redis | memory) and both object codecs (cache.codec:
// json | cbor): objects, TTLs, atomic NX, counters, raw bytes and
// cache-aside loading with GetOrLoad.
//
//	go run ./_examples/cache -config _examples/cache/config.yaml -envfile _examples/cache/standalone.env
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
		slog.Error("[Fatal ERROR] unable to load config", "error", err)
		os.Exit(1)
	}

	// With driver: redis and Redis down, NewDeps stops here ("unable to setup
	// cache"), like a database that cannot be reached at startup.
	server := newServer(deps.NewDeps(*cfg), newCatalog())
	if err := server.Start(); err != nil {
		server.Shutdown()
		os.Exit(1)
	}
}

// newServer builds the server with every route; the tests drive it without
// listening on a port and with their own catalog.
func newServer(dps deps.Deps, products *catalog) *nhttp.ServerHttp {
	server := nhttp.NewHTTPServer(dps)
	registerRoutes(server, products)
	return server
}
