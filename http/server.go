package nhttp

import (
	"context"
	"log/slog"
	stdhttp "net/http"
	"os/signal"
	"strconv"
	"syscall"

	router "github.com/ams-soft/go-ams-router"
	"github.com/ams-soft/go-ams-router/middleware"
	"github.com/ams-soft/nforge/codec"
	"github.com/ams-soft/nforge/config"
	"github.com/ams-soft/nforge/deps"
)

type ServerHttp struct {
	Deps              *deps.Deps
	Srv               *stdhttp.Server
	globalMiddlewares []func(stdhttp.Handler) stdhttp.Handler
	ShutdownFunc      []func()
	// TypedRoutes holds the typed routes (and those from AddDocRoute), the source
	// for the documentation generator.
	TypedRoutes []TypedRoute

	specs specCache
	graph graphPage //nolint:unused // used by graph.go, which only the nforge_graph build compiles
}

// NewHTTPServer builds the server on deps, which it keeps and shares with the
// caller. A Deps built by hand without a Logger gets a discarding one.
func NewHTTPServer(deps *deps.Deps) *ServerHttp {
	if deps == nil {
		panic("nhttp: NewHTTPServer: nil Deps")
	}
	if deps.Logger == nil {
		deps.Logger = slog.New(slog.DiscardHandler)
	}
	deps.Logger.Info(" [HTTPModule] Starting HTTP server")

	cfg := deps.Config.HTTP.Server
	config.ApplyServerDefaults(&cfg) // a Config built without config.Load has zero timeouts

	server := &ServerHttp{
		Deps: deps,
		Srv: &stdhttp.Server{
			Addr:              cfg.Host + ":" + strconv.Itoa(cfg.Port),
			ReadHeaderTimeout: cfg.ReadHeaderTimeout,
			ReadTimeout:       cfg.ReadTimeout,
			WriteTimeout:      cfg.WriteTimeout,
			IdleTimeout:       cfg.IdleTimeout,
		},
	}
	server.setStandardHandler()

	return server
}

func (s *ServerHttp) AddShutdownFunc(fn func()) {
	s.ShutdownFunc = append(s.ShutdownFunc, fn)
}

func (s *ServerHttp) AddGlobalMiddleware(middleware func(stdhttp.Handler) stdhttp.Handler) {
	s.globalMiddlewares = append(s.globalMiddlewares, middleware)
}

func (s *ServerHttp) setStandardHandler() {
	handler := router.NewRouter()
	s.Srv.Handler = handler // s.Typed()/s.Router() read the router from here

	// essential middlewares: Tracing, Logger and Recoverer, then the optional
	// Security and CORS (before routing, so preflights reach every path)
	if s.Deps.Config.IsTracingEnabled() {
		var tracingOpts []TracingOption
		if s.Deps.Config.Tracing.Otel.TrustsIncoming() {
			tracingOpts = append(tracingOpts, WithTrustedIncomingContext())
		}
		handler.Use(Tracing(tracingOpts...))
	}
	if s.Deps.Config.Logging.Output.Otel.Enabled || s.Deps.Config.Logging.Output.Terminal.Enabled {
		handler.Use(Logger(s.Deps.Logger))
	}
	handler.Use(middleware.Recoverer)
	limits := s.Deps.Config.HTTP.Server
	config.ApplyServerDefaults(&limits)
	handler.Use(bodyLimit(s.maxBodyBytes(limits)))
	handler.Use(handlerTimeout(limits.HandlerTimeout))
	sec := s.Deps.Config.HTTP.Security
	config.ApplySecurityDefaults(&sec) // a Config built without config.Load has none
	if sec.IsEnabled() {
		handler.Use(middleware.Security(securityOptions(sec)))
	}
	if s.Deps.Config.HTTP.CORS.Enabled {
		handler.Use(middleware.CORS(corsOptions(s.Deps.Config.HTTP.CORS)))
	}

	if s.Deps.Config.HTTP.Docs.Enabled {
		s.registerDocs(handler)
	}
	if s.Deps.JOSE != nil && s.Deps.Config.JOSE.JWKS.Enabled {
		s.registerJWKS(handler)
	}

	if s.Deps.Config.HTTP.Routes.Graph.Enabled {
		s.registerGraph(handler)
	}

	if s.Deps.Config.HTTP.Routes.Health.Enabled || s.Deps.Config.HTTP.Routes.Readiness.Enabled {
		s.Typed().Route("/", func(api *TypedRouter) {
			setRoutes(api, s.Deps)
		})
	}

}

// Router returns the server's router, for registering application routes.
func (s *ServerHttp) Router() router.Router {
	r, _ := s.Srv.Handler.(router.Router)
	return r
}

func (s *ServerHttp) LoadGlobalMiddleware() {
	s.Deps.Logger.Info(" [HTTPModule] Loading global middlewares")
	if r, ok := s.Srv.Handler.(router.Router); ok {

		r.Use(s.globalMiddlewares...)
	}
}

func (s *ServerHttp) Shutdown() {
	for _, fn := range s.ShutdownFunc {
		fn()
	}
}

// Start serves every enabled transport (plain HTTP, HTTPS, HTTP/3) and blocks
// until SIGINT/SIGTERM or a listener fails; see StartContext.
func (s *ServerHttp) Start() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return s.StartContext(ctx)
}

// maxBodyBytes is http.server.max_body_bytes, or the codec payload limit when
// it is unset.
func (s *ServerHttp) maxBodyBytes(cfg config.ServerConfig) int64 {
	if cfg.MaxBodyBytes > 0 {
		return int64(cfg.MaxBodyBytes)
	}
	if limit := int64(s.Deps.Config.Codec.MaxPayloadBytes); limit > 0 {
		return limit
	}
	return codec.DefaultMaxPayloadBytes
}
