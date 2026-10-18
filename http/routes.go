package nhttp

import (
	"context"
	stdhttp "net/http"
	"strings"
	"time"

	router "github.com/ams-soft/go-ams-router"
	"github.com/ams-soft/nforge/config"
	"github.com/ams-soft/nforge/deps"
	"github.com/ams-soft/nforge/http/internal/jwks"
	"github.com/ams-soft/nforge/http/internal/openapi"
)

// Built-in routes registered by NewHTTPServer: health/readiness, the
// documentation (page + OpenAPI spec) and the JWKS endpoint.

type HealthResponse struct {
	Status string `json:"status"`
}

type DatabaseInfo struct {
	Health bool `json:"health"`
}

// JOSEInfo reports the JOSE service state in the readiness response.
type JOSEInfo struct {
	Health bool `json:"health"`
}

// CacheInfo reports the cache backend state in the readiness response.
type CacheInfo struct {
	Health bool `json:"health"`
}

type ReadinessResponse struct {
	Status string `json:"status,omitempty"`
	// Database is omitted when database.enabled is false.
	Database *DatabaseInfo `json:"database,omitempty"`
	// JOSE is omitted when jose.enabled is false.
	JOSE *JOSEInfo `json:"jose,omitempty"`
	// Cache is omitted when cache.enabled is false.
	Cache       *CacheInfo `json:"cache,omitempty"`
	OnlineSince string     `json:"online_since"`
}

// readinessTimeout bounds the database and cache pings of the readiness
// route, so a hung dependency makes the probe fail fast instead of piling up
// requests.
const readinessTimeout = 2 * time.Second

func setRoutes(api *TypedRouter, d *deps.Deps) {
	cfg := d.Config
	tag := " maintenance"
	if cfg.HTTP.Routes.Health.Enabled {
		api.TGet(cfg.HTTP.Routes.Health.Path, nil, HealthResponse{}, func(w stdhttp.ResponseWriter, req *stdhttp.Request) {
			_ = SendJSON(w, req, stdhttp.StatusOK, HealthResponse{Status: "ok"})
		}, Opts{DocTag: tag, Description: "Health check"})
	}

	if cfg.HTTP.Routes.Readiness.Enabled {
		api.TGet(cfg.HTTP.Routes.Readiness.Path, nil, ReadinessResponse{}, func(w stdhttp.ResponseWriter, req *stdhttp.Request) {
			resp := ReadinessResponse{Status: "ok", OnlineSince: cfg.App.OnlineSince}
			healthy := true
			if d.DB != nil {
				ctx, cancel := context.WithTimeout(req.Context(), readinessTimeout)
				defer cancel()
				err := d.PingDatabase(ctx)
				resp.Database = &DatabaseInfo{Health: err == nil}
				if err != nil {
					d.Logger.WarnContext(req.Context(), "[HTTPModule] Readiness: database unavailable", "error", err)
					healthy = false
				}
			}
			if d.JOSE != nil {
				err := d.CheckJOSE(req.Context())
				resp.JOSE = &JOSEInfo{Health: err == nil}
				if err != nil {
					d.Logger.WarnContext(req.Context(), "[HTTPModule] Readiness: jose unavailable", "error", err)
					healthy = false
				}
			}
			if d.Cache != nil {
				// pinged on every probe: a recovered Redis is healthy again at once
				ctx, cancel := context.WithTimeout(req.Context(), readinessTimeout)
				defer cancel()
				err := d.PingCache(ctx)
				resp.Cache = &CacheInfo{Health: err == nil}
				if err != nil {
					d.Logger.WarnContext(req.Context(), "[HTTPModule] Readiness: cache unavailable", "error", err)
					healthy = false
				}
			}
			if !healthy {
				resp.Status = "unavailable"
				_ = SendJSON(w, req, stdhttp.StatusServiceUnavailable, resp)
				return
			}
			_ = SendJSON(w, req, stdhttp.StatusOK, resp)
		}, Opts{DocTag: tag, Description: "Readiness check: 503 when the database, jose or the cache is unavailable"})
	}
}

// docsBase is the normalized documentation path ("/docs" by default).
func docsBase(cfg config.DocsConfig) string {
	base := "/" + strings.Trim(cfg.Path, "/")
	if base == "/" {
		base = "/docs"
	}
	return base
}

// registerDocs serves the documentation at {path} (page), {path}/swagger.json
// and {path}/swagger.yaml. The spec is generated on every request from
// TypedRoutes, so it includes routes registered after the server was created.
func (s *ServerHttp) registerDocs(r router.Router) {
	base := docsBase(s.Deps.Config.HTTP.Docs)

	r.Get(base, func(w stdhttp.ResponseWriter, req *stdhttp.Request) {
		cfg := s.Deps.Config.HTTP.Docs
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
			s.Deps.Logger.ErrorContext(req.Context(), "failed to render docs", "error", err)
			stdhttp.Error(w, "failed to render docs", stdhttp.StatusInternalServerError)
			return
		}
		if header := docsCSPHeader(s.Deps.Config.HTTP.Security); header != "" {
			w.Header().Set(header, csp)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(page)
	})
	r.Get(base+"/swagger.json", s.serveSpec("application/json", (*openapi.Doc).JSON))
	r.Get(base+"/swagger.yaml", s.serveSpec("application/yaml", (*openapi.Doc).YAML))
}

func (s *ServerHttp) serveSpec(contentType string, render func(*openapi.Doc) ([]byte, error)) stdhttp.HandlerFunc {
	return func(w stdhttp.ResponseWriter, req *stdhttp.Request) {
		data, err := render(buildOpenAPI(s.Deps.Config.HTTP.Docs, s.TypedRoutes))
		if err != nil {
			s.Deps.Logger.ErrorContext(req.Context(), "failed to generate OpenAPI spec", "error", err)
			stdhttp.Error(w, "failed to generate spec", stdhttp.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", contentType+"; charset=utf-8")
		_, _ = w.Write(data)
	}
}

// buildOpenAPI builds the spec of the registered typed routes.
func buildOpenAPI(cfg config.DocsConfig, routes []TypedRoute) *openapi.Doc {
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
	return openapi.Build(cfg, docs)
}

// registerJWKS serves the public JSON Web Key Set at jose.jwks.path (GET and
// HEAD). It is a protocol endpoint: raw application/jwk-set+json, no
// envelope, no typed-route capture, with a strong ETag for conditional GET.
func (s *ServerHttp) registerJWKS(r router.Router) {
	cfg := s.Deps.Config
	path := cfg.JOSE.JWKS.Path
	reserved := map[string]string{}
	if cfg.HTTP.Docs.Enabled {
		base := docsBase(cfg.HTTP.Docs)
		for _, p := range []string{base, base + "/swagger.json", base + "/swagger.yaml"} {
			reserved[p] = "http.docs"
		}
	}
	if cfg.HTTP.Routes.Health.Enabled {
		reserved[cfg.HTTP.Routes.Health.Path] = "http.routes.health"
	}
	if cfg.HTTP.Routes.Readiness.Enabled {
		reserved[cfg.HTTP.Routes.Readiness.Path] = "http.routes.readiness"
	}
	if owner, ok := reserved[path]; ok {
		panic("nhttp: jose.jwks.path " + path + " collides with " + owner)
	}

	maxAge := time.Minute
	if cfg.JOSE.JWKS.CacheMaxAge != nil {
		maxAge = *cfg.JOSE.JWKS.CacheMaxAge
	}
	handler := jwks.Handler(s.Deps.JOSE.PublicJWKS, maxAge, func(ctx context.Context, err error) {
		s.Deps.Logger.WarnContext(ctx, "[HTTPModule] JWKS unavailable", "error", err)
	})
	r.Get(path, handler)
	r.Head(path, handler)
}
