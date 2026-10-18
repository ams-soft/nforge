package nhttp

import (
	"context"
	"errors"
	stdhttp "net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	router "github.com/ams-soft/go-ams-router"
	"github.com/ams-soft/go-ams-router/middleware"
	"github.com/ams-soft/nforge/config"
	"github.com/ams-soft/nforge/deps"
)

type ServerHttp struct {
	Deps              deps.Deps
	Srv               *stdhttp.Server
	globalMiddlewares []func(stdhttp.Handler) stdhttp.Handler
	ShutdownFunc      []func()
	// TypedRoutes holds the typed routes (and those from AddDocRoute), the source
	// for the documentation generator.
	TypedRoutes []TypedRoute
}

func NewHTTPServer(deps deps.Deps) *ServerHttp {

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
		handler.Use(Tracing())
	}
	if s.Deps.Config.Logging.Output.Otel.Enabled || s.Deps.Config.Logging.Output.Terminal.Enabled {
		handler.Use(Logger(&s.Deps.Logger))
	}
	handler.Use(middleware.Recoverer)
	if s.Deps.Config.HTTP.Security.Enabled {
		handler.Use(middleware.Security(securityOptions(s.Deps.Config.HTTP.Security)))
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

	if s.Deps.Config.HTTP.Routes.Health.Enabled || s.Deps.Config.HTTP.Routes.Readiness.Enabled {
		s.Typed().Route("/", func(api *TypedRouter) {
			setRoutes(api, &s.Deps)
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

func (s *ServerHttp) Start() error {
	errCh := make(chan error, 1)
	go func() {
		s.Deps.Logger.Info("[HTTPModule] Server running", "addr", s.Srv.Addr)
		if err := s.Srv.ListenAndServe(); err != nil && !errors.Is(err, stdhttp.ErrServerClosed) {
			errCh <- err
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	select {
	case err := <-errCh:
		s.Deps.Logger.Error("[HTTPModule] Failed to start server", "err", err)
		return err
	case <-quit:
	}

	s.Deps.Logger.Info("[HTTPModule] Shutting down server...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	srvErr := s.Srv.Shutdown(ctx)
	if srvErr != nil {
		s.Deps.Logger.Error("[HTTPModule] Forced shutdown", "err", srvErr)
	} else {
		s.Deps.Logger.Info("[HTTPModule] Server stopped successfully")
	}

	// Only after we stop accepting requests: close database, tracing and logs
	// (in that order, LIFO). Logs from here on may be lost.
	return errors.Join(srvErr, s.Deps.Shutdown(ctx))
}
