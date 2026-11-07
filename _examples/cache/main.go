// Command cache shows the cache module with the same code for both drivers
// (cache.driver: redis | memory) and both object codecs (cache.codec:
// json | cbor): objects, TTLs, atomic NX, counters, raw bytes and
// cache-aside loading with GetOrLoad.
//
//	go run ./_examples/cache -config _examples/cache/config.yaml -envfile _examples/cache/standalone.env
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/ams-soft/nforge/app"
	"github.com/ams-soft/nforge/cache"
	"github.com/ams-soft/nforge/codec"
	"github.com/ams-soft/nforge/config"
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
	application, err := newApp(cfg, newCatalog())
	if err != nil {
		fmt.Fprintln(os.Stderr, "[Fatal ERROR]", err)
		os.Exit(1)
	}
	// With driver: redis and Redis down, the start stops here ("unable to setup
	// cache"), like a database that cannot be reached at startup.
	if err := application.Run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "[Fatal ERROR]", err)
		os.Exit(1)
	}
}

// newApp registers the modules; the tests reuse it with their own catalog.
func newApp(cfg *config.Document, products *catalog, opts ...app.Option) (*app.Application, error) {
	routes := module.New("routes", module.Definition{
		Requires: []module.Handle{nhttp.Capability, cache.Capability},
		Optional: []module.Handle{module.Logger},
		Init: func(_ context.Context, s *module.Scope) (module.Hooks, error) {
			logger, ok := module.Lookup(s, module.Logger)
			if !ok {
				logger = slog.New(slog.DiscardHandler)
			}
			registerRoutes(module.Require(s, nhttp.Capability), module.Require(s, cache.Capability), logger, products)
			return module.Hooks{}, nil
		},
	})
	mods := app.Modules(logs.Module(), codec.Module(), cache.Module(), nhttp.Module(), routes)
	return app.New(cfg, append([]app.Option{mods}, opts...)...)
}
