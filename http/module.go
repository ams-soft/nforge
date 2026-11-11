package nhttp

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/ams-soft/nforge/cache"
	"github.com/ams-soft/nforge/codec"
	"github.com/ams-soft/nforge/database"
	"github.com/ams-soft/nforge/jose"
	"github.com/ams-soft/nforge/message"
	"github.com/ams-soft/nforge/module"
	"github.com/ams-soft/nforge/telemetry"
)

// Capability is the application's HTTP server, published by Module. An
// application module declares it in Requires and registers its routes and
// middlewares on it during Init; the listeners only open after every Init.
var Capability = module.NewCapability[*Server]("http")

// Module is the HTTP module: it owns the http section, builds the Server at
// Init (router, standard middleware chain, docs, JWKS, health, readiness and
// graph routes) and publishes it under Capability. It is an ingress module:
// it binds and serves only after every other module started, so no request can
// reach a handler before it is registered, and it stops first, draining the
// requests in flight before any resource they use is closed.
//
// It requires the codec module, and the jose module when
// http.auth.default_profile is set; logging, tracing, jose, the database and
// the cache are optional (the last two only for the TLS posture checks).
func Module() module.Descriptor {
	return module.Descriptor{ID: "http", Section: "http", Configure: func(src module.Source) (module.Definition, error) {
		var cfg Config
		if err := src.Decode(&cfg); err != nil {
			return module.Definition{}, err
		}
		app := src.App()
		cfg.ApplyDefaults(app)
		if err := cfg.Validate(); err != nil {
			return module.Definition{}, err
		}
		for _, f := range warnings(&cfg, app) {
			src.Warn(f.exception, f.msg)
		}
		requires := []module.Handle{codec.Capability}
		if cfg.Auth.DefaultProfile != "" {
			requires = append(requires, jose.Capability)
		}
		return module.Definition{
			Provides: []module.Handle{Capability},
			Requires: requires,
			Optional: []module.Handle{module.Logger, telemetry.Capability, jose.Capability, database.Capability, cache.Capability, message.Capability},
			Ingress:  true,
			Config:   cfg,
			Check:    func(v *module.View) error { return check(v, &cfg) },
			Init: func(ctx context.Context, sc *module.Scope) (module.Hooks, error) {
				d := Dependencies{App: app, Codec: module.Require(sc, codec.Capability), Status: sc.Introspect(), Failed: sc.ReportFailure}
				d.Logger, _ = module.Lookup(sc, module.Logger)
				// a logger that discards errors has no output: no request log either
				d.AccessLog = d.Logger != nil && d.Logger.Handler().Enabled(ctx, slog.LevelError)
				d.Tracing, _ = module.Lookup(sc, telemetry.Capability)
				d.JOSE, _ = module.Lookup(sc, jose.Capability)
				s := New(cfg, d)
				if cfg.Server.Header {
					s.banner(ctx)
				}
				s.logger.InfoContext(ctx, " [HTTPModule] HTTP server ready for route registration")
				module.Provide(sc, Capability, s)
				return module.Hooks{Start: s.Start, Stop: s.Stop, Reload: s.Reload}, nil
			},
		}, nil
	}}
}

// check validates what involves the other modules: the default auth profile
// against the jose profiles, the JWKS path against the built-in routes, and
// the TLS posture of the backends behind an HTTPS edge.
func check(v *module.View, cfg *Config) error {
	jcfg, joseOn, _, err := module.ConfigOf[jose.Config](v, jose.Capability)
	if err != nil {
		return err
	}
	if name := cfg.Auth.DefaultProfile; name != "" {
		if !joseOn {
			return fmt.Errorf("http.auth.default_profile requires jose.enabled: true")
		}
		if err := checkProfile(jcfg, name); err != nil {
			return err
		}
	}
	jwks := ""
	if joseOn && jcfg.JWKS.Enabled {
		jwks = jcfg.JWKS.Path
		if owner := reservedBy(cfg, jwks); owner != "" {
			return fmt.Errorf("jose.jwks.path %s collides with %s", jwks, owner)
		}
	}
	if err := ValidateGraphRoute(cfg, jwks); err != nil {
		return err
	}
	if cfg.Server.TLS.Enabled || cfg.Server.HTTP3.Enabled {
		mc, enabled, _, err := module.ConfigOf[message.Config](v, message.Capability)
		if err != nil {
			return err
		}
		if enabled && !mc.UsesTLS() {
			v.Warn("message_plaintext", "the server uses HTTPS or HTTP/3 but RabbitMQ is reached without TLS; use an amqps:// URL")
		}
	}

	var c *cache.Config
	if cc, on, _, err := module.ConfigOf[cache.Config](v, cache.Capability); err != nil {
		return err
	} else if on {
		c = &cc
	}
	var d *database.Config
	if dc, on, _, err := module.ConfigOf[database.Config](v, database.Capability); err != nil {
		return err
	} else if on {
		d = &dc
	}
	for _, f := range edgeTLSWarnings(cfg, c, d) {
		v.Warn(f.exception, f.msg)
	}
	return nil
}

func checkProfile(jcfg jose.Config, name string) error {
	for _, p := range jcfg.Profiles {
		if p.Name != name {
			continue
		}
		if p.Kind != jose.KindJWTSigned && p.Kind != jose.KindJWTNested {
			return fmt.Errorf("http.auth.default_profile %q must be a jwt_signed or jwt_nested profile", name)
		}
		if len(p.VerificationKeys) == 0 {
			return fmt.Errorf("http.auth.default_profile %q cannot validate tokens", name)
		}
		return nil
	}
	return fmt.Errorf("http.auth.default_profile %q is not a configured jose profile", name)
}

// reservedBy names the built-in route that already serves path, if any.
func reservedBy(cfg *Config, path string) string {
	for _, b := range builtinPaths(cfg, "") {
		if b.path == path {
			return b.owner
		}
	}
	return ""
}
