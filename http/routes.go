package nhttp

import (
	"context"
	stdhttp "net/http"
	"strings"
	"time"

	router "github.com/ams-soft/go-ams-router"
	"github.com/ams-soft/nforge/http/internal/jwks"
	"github.com/ams-soft/nforge/http/internal/openapi"
	"github.com/ams-soft/nforge/module"
)

// Built-in routes registered by NewHTTPServer: health/readiness, the
// documentation (page + OpenAPI spec) and the JWKS endpoint.

type HealthResponse struct {
	Status string `json:"status"`
}

// ReadinessResponse is the body of the readiness route.
type ReadinessResponse struct {
	// Status is "ok" when the application is ready, "unavailable" otherwise.
	Status      string `json:"status"`
	OnlineSince string `json:"online_since"`
	// Modules maps every enabled module to ready or not_ready. It names the
	// modules, as the old database/jose/cache keys did: keep the route behind
	// the gateway.
	Modules map[string]string `json:"modules,omitempty"`
}

// readinessTimeout bounds how long a readiness request waits for the shared
// probe (which has its own bound), so a hung dependency makes the probe fail
// fast instead of piling up requests.
const readinessTimeout = 2 * time.Second

func (s *Server) setRoutes(api *TypedRouter) {
	cfg := s.cfg
	tag := " maintenance"
	if cfg.Routes.Health.Enabled {
		api.TGet(cfg.Routes.Health.Path, nil, HealthResponse{}, func(w stdhttp.ResponseWriter, req *stdhttp.Request) {
			_ = SendJSON(w, req, stdhttp.StatusOK, HealthResponse{Status: "ok"})
		}, Opts{DocTag: tag, Description: "Health check (liveness): never touches the dependencies"})
	}

	if cfg.Routes.Readiness.Enabled {
		api.TGet(cfg.Routes.Readiness.Path, nil, ReadinessResponse{}, func(w stdhttp.ResponseWriter, req *stdhttp.Request) {
			resp := ReadinessResponse{Status: "ok", OnlineSince: s.app.OnlineSince.UTC().Format(time.RFC3339)}
			if s.status == nil {
				_ = SendJSON(w, req, stdhttp.StatusOK, resp)
				return
			}
			ctx, cancel := context.WithTimeout(req.Context(), readinessTimeout)
			defer cancel()
			report := s.status.Status(ctx)
			resp.Modules = map[string]string{}
			for _, m := range report.Modules {
				if m.Availability == module.Disabled {
					continue
				}
				resp.Modules[string(m.ID)] = string(m.Availability)
				if m.Availability == module.NotReady {
					s.logger.WarnContext(req.Context(), "[HTTPModule] Readiness: module not ready", "module", string(m.ID))
				}
			}
			if !report.Ready {
				resp.Status = "unavailable"
				_ = SendJSON(w, req, stdhttp.StatusServiceUnavailable, resp)
				return
			}
			_ = SendJSON(w, req, stdhttp.StatusOK, resp)
		}, Opts{DocTag: tag, Description: "Readiness check: 503 until the application is started and while a critical module is not ready"})
	}
}

// docsBase is the normalized documentation path ("/docs" by default).
func docsBase(cfg DocsConfig) string {
	base := "/" + strings.Trim(cfg.Path, "/")
	if base == "/" {
		base = "/docs"
	}
	return base
}

// registerDocs serves the documentation at {path} (page), {path}/swagger.json
// and {path}/swagger.yaml. The spec is generated on every request from
// TypedRoutes, so it includes routes registered after the server was created.
func (s *Server) registerDocs(r router.Router) {
	base := docsBase(s.cfg.Docs)

	r.Get(base, func(w stdhttp.ResponseWriter, req *stdhttp.Request) {
		cfg := s.cfg.Docs
		page, csp, err := openapi.RenderPage(openapi.Page{
			Title:   cfg.Title,
			Version: cfg.Version,
			SpecURL: base + "/swagger.json",
			YAMLURL: base + "/swagger.yaml",
			Routes:  len(s.TypedRoutes),
			Accent:  cfg.Accent,
			Hosts:   cfg.Hosts,
		})
		if err != nil {
			s.logger.ErrorContext(req.Context(), "failed to render docs", "error", err)
			stdhttp.Error(w, "failed to render docs", stdhttp.StatusInternalServerError)
			return
		}
		if header := docsCSPHeader(s.cfg.Security); header != "" {
			w.Header().Set(header, csp)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(page)
	})
	r.Get(base+"/swagger.json", s.serveSpec("application/json", (*openapi.Doc).JSON))
	r.Get(base+"/swagger.yaml", s.serveSpec("application/yaml", (*openapi.Doc).YAML))
}

func (s *Server) serveSpec(contentType string, render func(*openapi.Doc) ([]byte, error)) stdhttp.HandlerFunc {
	return func(w stdhttp.ResponseWriter, req *stdhttp.Request) {
		spec, err := s.specs.get(contentType, len(s.TypedRoutes), func() ([]byte, error) {
			return render(buildOpenAPI(s.cfg.Docs, s.TypedRoutes))
		})
		if err != nil {
			s.logger.ErrorContext(req.Context(), "failed to generate OpenAPI spec", "error", err)
			stdhttp.Error(w, "failed to generate spec", stdhttp.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", contentType+"; charset=utf-8")
		w.Header().Set("ETag", spec.etag)
		w.Header().Set("Cache-Control", "no-cache") // reuse only after revalidating
		if req.Header.Get("If-None-Match") == spec.etag {
			w.WriteHeader(stdhttp.StatusNotModified)
			return
		}
		_, _ = w.Write(spec.data)
	}
}

// buildOpenAPI builds the spec of the registered typed routes.
func buildOpenAPI(cfg DocsConfig, routes []TypedRoute) *openapi.Doc {
	docs := make([]openapi.Route, len(routes))
	for i, rt := range routes {
		docs[i] = openapi.Route{
			Method:         rt.Method,
			Path:           rt.Path,
			Description:    rt.Description,
			Tag:            rt.Tag,
			Headers:        rt.Headers,
			IsPrivate:      rt.IsPrivate,
			AuthProfile:    rt.AuthProfile,
			RequiredScopes: rt.RequiredScopes,
			Tenant:         rt.Tenant,
			TenantHeader:   rt.TenantHeader,
			Codec:          rt.Codec,
			Schema:         rt.Schema,
			Response:       rt.Response,
		}
	}
	return openapi.Build(openapi.Info{Title: cfg.Title, Version: cfg.Version, Auth: cfg.Auth, Hosts: cfg.Hosts}, docs)
}

// registerJWKS serves the public JSON Web Key Set at jose.jwks.path (GET and
// HEAD). It is a protocol endpoint: raw application/jwk-set+json, no
// envelope, no typed-route capture, with a strong ETag for conditional GET.
func (s *Server) registerJWKS(r router.Router) {
	cfg := s.cfg
	jwksCfg := s.jose.JWKS()
	path := jwksCfg.Path
	reserved := map[string]string{}
	if cfg.Docs.Enabled {
		base := docsBase(cfg.Docs)
		for _, p := range []string{base, base + "/swagger.json", base + "/swagger.yaml"} {
			reserved[p] = "http.docs"
		}
	}
	if cfg.Routes.Health.Enabled {
		reserved[cfg.Routes.Health.Path] = "http.routes.health"
	}
	if cfg.Routes.Readiness.Enabled {
		reserved[cfg.Routes.Readiness.Path] = "http.routes.readiness"
	}
	if owner, ok := reserved[path]; ok {
		panic("nhttp: jose.jwks.path " + path + " collides with " + owner)
	}

	maxAge := time.Minute
	if jwksCfg.CacheMaxAge != nil {
		maxAge = *jwksCfg.CacheMaxAge
	}
	handler := jwks.Handler(s.jose.PublicJWKS, maxAge, func(ctx context.Context, err error) {
		s.logger.WarnContext(ctx, "[HTTPModule] JWKS unavailable", "error", err)
	})
	r.Get(path, handler)
	r.Head(path, handler)
}
