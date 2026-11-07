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
	"fmt"
	"log/slog"
	stdhttp "net/http"
	"os"
	"time"

	"github.com/ams-soft/nforge/app"
	"github.com/ams-soft/nforge/cache"
	"github.com/ams-soft/nforge/codec"
	"github.com/ams-soft/nforge/config"
	"github.com/ams-soft/nforge/database"
	nhttp "github.com/ams-soft/nforge/http"
	"github.com/ams-soft/nforge/jose"
	"github.com/ams-soft/nforge/logs"
	"github.com/ams-soft/nforge/module"
	"github.com/ams-soft/nforge/telemetry"
)

func main() {
	cfg, err := config.Load() // -config and -envfile flags; ./config.yaml by default
	if err != nil {
		fmt.Fprintln(os.Stderr, "[Fatal ERROR] unable to load config:", err)
		os.Exit(1)
	}
	application, err := newApp(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "[Fatal ERROR]", err)
		os.Exit(1)
	}
	// Run starts every module, serves, and stops gracefully on SIGINT/SIGTERM;
	// SIGHUP reloads the TLS certificates and the JOSE keys.
	if err := application.Run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "[Fatal ERROR]", err)
		os.Exit(1)
	}
}

// newApp registers the modules explicitly: the configuration can disable a
// registered module (database.enabled: false), never add one. The tests reuse
// it with their own configuration.
func newApp(cfg *config.Document, opts ...app.Option) (*app.Application, error) {
	mods := app.Modules(
		logs.Module(), telemetry.Module(), codec.Module(),
		database.Module(), jose.Module(), cache.Module(),
		nhttp.Module(), apiModule(),
	)
	return app.New(cfg, append([]app.Option{mods}, opts...)...)
}

// env is what the routes of this example use, taken from the module graph.
type env struct {
	server *nhttp.Server
	logger *slog.Logger
	jose   *jose.Service
	cache  *cache.Cache
	db     *database.DB // nil with database.enabled: false
	// accessTTL is the lifetime of the access tokens, read from the jose
	// configuration when the graph is checked.
	accessTTL time.Duration
}

// apiModule is the application module: it declares what it uses and
// registers every route during Init, before any listener opens.
func apiModule() module.Descriptor {
	var ttl time.Duration
	return module.New("api", module.Definition{
		Requires: []module.Handle{nhttp.Capability, jose.Capability, cache.Capability},
		Optional: []module.Handle{module.Logger, database.Capability},
		Check: func(v *module.View) error {
			cfg, _, _, err := module.ConfigOf[jose.Config](v, jose.Capability)
			for _, p := range cfg.Profiles {
				if p.Name == accessProfile {
					ttl = p.JWT.TTL
				}
			}
			return err
		},
		Init: func(ctx context.Context, s *module.Scope) (module.Hooks, error) {
			e := env{
				server: module.Require(s, nhttp.Capability),
				jose:   module.Require(s, jose.Capability),
				cache:  module.Require(s, cache.Capability),
				logger: slog.New(slog.DiscardHandler), accessTTL: ttl,
			}
			if l, ok := module.Lookup(s, module.Logger); ok {
				e.logger = l
			}
			e.db, _ = module.Lookup(s, database.Capability)

			// uncomment to see the crypto demo
			// runCrypto(e.logger)

			// Global middlewares run after the built-in ones (tracing, logger,
			// recoverer) and must be added before the first request.
			e.server.Use(clacksOverhead)

			registerPlainRoutes(e.server, e.logger)
			registerAuthRoutes(e)
			registerCacheRoutes(e)
			if err := registerDatabaseRoutes(ctx, e); err != nil {
				return module.Hooks{}, err
			}
			e.server.Typed().Route("/api/v1", func(api *nhttp.TypedRouter) {
				registerUserRoutes(api, newUserStore())
			})
			return module.Hooks{}, nil
		},
	})
}

// clacksOverhead is a minimal global middleware: it only adds a header.
func clacksOverhead(next stdhttp.Handler) stdhttp.Handler {
	return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		w.Header().Set("X-Clacks-Overhead", "GNU Terry Pratchett")
		next.ServeHTTP(w, r)
	})
}
