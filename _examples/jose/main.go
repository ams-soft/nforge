// Command jose shows nForge's JOSE support: a demo login that issues JWTs,
// raw and typed routes protected by bearer authentication and scopes,
// nested (signed + encrypted) tokens and a generic JWS/JWE round trip.
//
//	export JWT_SECRET=$(openssl rand -hex 32) JWT_ENCRYPTION_SECRET=$(openssl rand -hex 16)
//	go run ./_examples/jose -config _examples/jose/config.yaml
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
	nhttp "github.com/ams-soft/nforge/http"
	"github.com/ams-soft/nforge/jose"
	"github.com/ams-soft/nforge/logs"
	"github.com/ams-soft/nforge/module"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "[Fatal ERROR] unable to load config:", err)
		os.Exit(1)
	}
	application, err := newApp(cfg) // e.g. "secret or source is required" when JWT_SECRET is unset
	if err != nil {
		fmt.Fprintln(os.Stderr, "[Fatal ERROR]", err)
		os.Exit(1)
	}
	if err := application.Run(context.Background()); err != nil { // the jose module loads and validates every key
		fmt.Fprintln(os.Stderr, "[Fatal ERROR]", err)
		os.Exit(1)
	}
}

// newApp registers the modules; main_test.go reuses it.
func newApp(cfg *config.Document, opts ...app.Option) (*app.Application, error) {
	ttls := map[string]time.Duration{}
	routes := module.New("routes", module.Definition{
		Requires: []module.Handle{nhttp.Capability, jose.Capability},
		Optional: []module.Handle{module.Logger},
		// The token lifetimes come from the validated jose configuration.
		Check: func(v *module.View) error {
			jcfg, _, _, err := module.ConfigOf[jose.Config](v, jose.Capability)
			for _, p := range jcfg.Profiles {
				ttls[p.Name] = p.JWT.TTL
			}
			return err
		},
		Init: func(_ context.Context, s *module.Scope) (module.Hooks, error) {
			logger, ok := module.Lookup(s, module.Logger)
			if !ok {
				logger = slog.New(slog.DiscardHandler)
			}
			registerRoutes(module.Require(s, nhttp.Capability), module.Require(s, jose.Capability), logger, ttls)
			return module.Hooks{}, nil
		},
	})
	mods := app.Modules(logs.Module(), codec.Module(), jose.Module(), nhttp.Module(), routes)
	return app.New(cfg, append([]app.Option{mods}, opts...)...)
}
