package nhttp

import (
	"context"
	"fmt"
	"log/slog"
	stdhttp "net/http"
	"strconv"
	"strings"
	"sync"

	router "github.com/ams-soft/go-ams-router"
	"github.com/ams-soft/go-ams-router/middleware"
	"github.com/ams-soft/nforge"
	"github.com/ams-soft/nforge/codec"
	"github.com/ams-soft/nforge/jose"
	"github.com/ams-soft/nforge/module"
	"github.com/ams-soft/nforge/telemetry"
)

// Server is the HTTP transport of an application: one router shared by the
// HTTP, HTTPS and HTTP/3 listeners, with the standard middleware chain, the
// typed routes and the built-in routes. It does not own the application's
// lifetime: Module starts it after every module registered its routes and
// stops it first, and app.Run handles the signals. Use it through a pointer.
type Server struct {
	// Srv is the plain HTTP server; its Handler is the router every transport
	// serves. Settings changed on it before Start reach every transport.
	Srv *stdhttp.Server
	// TypedRoutes holds the typed routes (and those from AddDocRoute), the source
	// for the documentation generator.
	TypedRoutes []TypedRoute

	cfg      Config
	app      module.App
	logger   *slog.Logger
	codec    *codec.Registry
	jose     *jose.Service
	tracing  *telemetry.Tracing
	status   module.Introspection
	failed   func(error)
	accessed bool // request logging is on

	specs specCache
	graph graphPage //nolint:unused // used by graph.go, which only the nforge_graph build compiles

	mu   sync.Mutex
	run  *running
	wg   sync.WaitGroup
	stop sync.Once
}

// Dependencies are the collaborators of a Server. Module fills them from the
// module graph; a Server built with New outside an application (tests,
// embedding) passes what it has.
type Dependencies struct {
	// App is the application metadata (docs title, version, banner, readiness).
	App module.App
	// Logger logs the server. Nil discards.
	Logger *slog.Logger
	// AccessLog adds the request log middleware (one record per request).
	AccessLog bool
	// Codec is the Registry of the requests; nil uses codec.Default().
	Codec *codec.Registry
	// JOSE authenticates private typed routes and serves the JWKS; nil means
	// none (a private route then panics at registration).
	JOSE *jose.Service
	// Tracing adds the tracing middleware (request_id = trace_id); nil means
	// none.
	Tracing *telemetry.Tracing
	// Status feeds the readiness route and the graph page; nil reports the
	// server alone.
	Status module.Introspection
	// Failed is told when a listener fails after Start; nil ignores it.
	Failed func(error)
}

// New builds the server of cfg, with the defaults applied to a copy. Routes
// and middlewares can be added until it serves its first request.
func New(cfg Config, d Dependencies) *Server {
	cfg.ApplyDefaults(d.App)
	logger := d.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	s := &Server{
		cfg: cfg, app: d.App, logger: logger, codec: d.Codec, jose: d.JOSE, tracing: d.Tracing,
		status: d.Status, failed: d.Failed,
		accessed: d.AccessLog,
		Srv: &stdhttp.Server{
			Addr:              cfg.Server.Host + ":" + strconv.Itoa(cfg.Server.Port),
			ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout,
			ReadTimeout:       cfg.Server.ReadTimeout,
			WriteTimeout:      cfg.Server.WriteTimeout,
			IdleTimeout:       cfg.Server.IdleTimeout,
		},
	}
	s.setStandardHandler()
	return s
}

// Config returns the effective configuration (defaults applied).
func (s *Server) Config() Config { return s.cfg }

// Use adds global middlewares after the standard chain (§3.3 of AGENTS.md).
// Valid until the server serves its first request; application modules call it
// during their Init.
func (s *Server) Use(middlewares ...func(stdhttp.Handler) stdhttp.Handler) {
	s.Router().Use(middlewares...)
}

func (s *Server) setStandardHandler() {
	handler := router.NewRouter()
	s.Srv.Handler = handler // s.Typed()/s.Router() read the router from here

	// essential middlewares: Tracing, Logger and Recoverer, then the optional
	// Security and CORS (before routing, so preflights reach every path)
	if s.tracing != nil {
		opts := []TracingOption{WithTracerProvider(s.tracing.Provider)}
		if s.tracing.TrustIncoming {
			opts = append(opts, WithTrustedIncomingContext())
		}
		handler.Use(Tracing(opts...))
	}
	if s.accessed {
		handler.Use(Logger(s.logger))
	}
	handler.Use(middleware.Recoverer)
	if s.codec != nil && s.codec != codec.Default() {
		handler.Use(withRegistry(s.codec))
	}
	handler.Use(bodyLimit(s.maxBodyBytes()))
	handler.Use(handlerTimeout(s.cfg.Server.HandlerTimeout))
	if s.cfg.Security.IsEnabled() {
		handler.Use(middleware.Security(securityOptions(s.cfg.Security)))
	}
	if s.cfg.CORS.Enabled {
		handler.Use(middleware.CORS(corsOptions(s.cfg.CORS)))
	}

	if s.cfg.Docs.Enabled {
		s.registerDocs(handler)
	}
	if s.jose != nil && s.jose.JWKS().Enabled {
		s.registerJWKS(handler)
	}
	if s.cfg.Routes.Graph.Enabled {
		s.registerGraph(handler)
	}
	if s.cfg.Routes.Health.Enabled || s.cfg.Routes.Readiness.Enabled {
		s.Typed().Route("/", func(api *TypedRouter) {
			s.setRoutes(api)
		})
	}
}

// banner logs the nForge banner (http.server.header).
func (s *Server) banner(ctx context.Context) {
	str := fmt.Sprintf(" ***** AMS SOFT nForge v%s: %s v%s *****", nforge.Version, strings.ToUpper(s.app.Name), s.app.Version)
	s.logger.InfoContext(ctx, str)
	s.logger.InfoContext(ctx, strings.Repeat("=", len(str)+1))
}

// registry is the Registry of this server: the application's, or the process
// default that requests also use.
func (s *Server) registry() *codec.Registry {
	if s.codec != nil {
		return s.codec
	}
	return codec.Default()
}

// withRegistry makes the requests of the server use its Registry.
func withRegistry(registry *codec.Registry) func(stdhttp.Handler) stdhttp.Handler {
	return func(next stdhttp.Handler) stdhttp.Handler {
		return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
			next.ServeHTTP(w, r.WithContext(codec.WithRegistry(r.Context(), registry)))
		})
	}
}

// Router returns the server's router, for registering application routes.
func (s *Server) Router() router.Router {
	r, _ := s.Srv.Handler.(router.Router)
	return r
}

// maxBodyBytes is http.server.max_body_bytes, or the codec payload limit when
// it is unset.
func (s *Server) maxBodyBytes() int64 {
	if s.cfg.Server.MaxBodyBytes > 0 {
		return int64(s.cfg.Server.MaxBodyBytes)
	}
	return s.registry().MaxPayloadBytes()
}
