package nhttp

import (
	"errors"
	"log/slog"
	stdhttp "net/http"
	"strings"

	"github.com/ams-soft/nforge/codec"
	"github.com/ams-soft/nforge/database"
	"github.com/ams-soft/nforge/http/internal/authn"
)

// Static tenant response details.
const (
	detailTenantRequired = "tenant required"
	detailTenantInvalid  = "invalid tenant"
)

// errNoPrincipal is returned by TenantFromClaim when no authentication ran
// before it; the middleware answers 401.
var errNoPrincipal = errors.New("nhttp: no authenticated principal")

// TenantResolver extracts the raw tenant ID of a request. It returns "" (or
// an error matching database.ErrNoTenant) when the request carries none; the
// middleware validates the ID as a UUID v4. Any other error is an internal
// failure (500).
type TenantResolver func(r *stdhttp.Request) (string, error)

// TenantFromClaim reads Principal.TenantID, i.e. the tenant_id claim of the
// validated JWT (or whatever a PrincipalMapper put there). It must run after
// Authenticate or on a private typed route; without a Principal the request
// is answered 401.
func TenantFromClaim() TenantResolver {
	return func(r *stdhttp.Request) (string, error) {
		p, ok := PrincipalFromContext(r.Context())
		if !ok {
			return "", errNoPrincipal
		}
		return p.TenantID, nil
	}
}

// TenantFromHeader reads the tenant from a request header. A repeated header
// is rejected as invalid. A header is client-controlled: unless an upstream
// gateway sets it, the application must still check that the caller belongs
// to that tenant.
func TenantFromHeader(name string) TenantResolver {
	if !authn.ValidHeaderName(name) {
		panic("nhttp: TenantFromHeader: invalid header name " + name)
	}
	return func(r *stdhttp.Request) (string, error) {
		values := r.Header.Values(name)
		switch len(values) {
		case 0:
			return "", nil
		case 1:
			return strings.TrimSpace(values[0]), nil
		default:
			return "", database.ErrInvalidTenantID
		}
	}
}

// TenantOption configures Tenant.
type TenantOption func(*tenantGuard)

// WithTenantResolver replaces the default resolver (TenantFromClaim).
func WithTenantResolver(resolve TenantResolver) TenantOption {
	if resolve == nil {
		panic("nhttp: WithTenantResolver: nil resolver")
	}
	return func(g *tenantGuard) { g.resolve = resolve }
}

// WithTenantOptional lets requests without a tenant through (an invalid
// tenant is still rejected); handlers then get database.ErrNoTenant from
// DB.ForContext.
func WithTenantOptional() TenantOption {
	return func(g *tenantGuard) { g.optional = true }
}

// Tenant returns a middleware that makes sure the request context carries a
// tenant for database.DB.ForContext. A tenant already stored by an earlier
// middleware is kept; otherwise it is resolved (by default from the JWT
// tenant_id claim, so mount it after Authenticate) and stored. Missing
// tenant → 403, invalid (not a UUID v4) → 400, no Principal for the claim
// resolver → 401. Responses use the default codec.
func Tenant(opts ...TenantOption) func(stdhttp.Handler) stdhttp.Handler {
	g := &tenantGuard{resolve: TenantFromClaim(), realm: DefaultRealm, logger: slog.Default}
	for _, opt := range opts {
		if opt == nil {
			panic("nhttp: Tenant: nil option")
		}
		opt(g)
	}
	return g.middleware()
}

// SetTenant returns a middleware that always resolves the tenant with resolve
// and stores it, replacing any tenant already in the context. Use it to bind
// the tenant from a source of your own (subdomain, path, API key lookup);
// errors are answered like in Tenant.
func SetTenant(resolve TenantResolver) func(stdhttp.Handler) stdhttp.Handler {
	if resolve == nil {
		panic("nhttp: SetTenant: nil resolver")
	}
	g := &tenantGuard{resolve: resolve, override: true, realm: DefaultRealm, logger: slog.Default}
	return g.middleware()
}

// TenantMiddleware returns Tenant configured from http.tenant (source and
// header) and http.auth.realm, the same guard used by typed routes with
// Opts.Tenant, for raw routes and route groups.
func (s *ServerHttp) TenantMiddleware(opts ...TenantOption) func(stdhttp.Handler) stdhttp.Handler {
	g := s.tenantGuard()
	for _, opt := range opts {
		if opt == nil {
			panic("nhttp: TenantMiddleware: nil option")
		}
		opt(g)
	}
	return g.middleware()
}

// TenantID returns the tenant stored in the request context.
func TenantID(r *stdhttp.Request) (string, bool) {
	return database.TenantFromContext(r.Context())
}

// WithTenantID returns r with the validated tenant stored in its context.
func WithTenantID(r *stdhttp.Request, tenantID string) (*stdhttp.Request, error) {
	ctx, err := database.WithTenant(r.Context(), tenantID)
	if err != nil {
		return r, err
	}
	return r.WithContext(ctx), nil
}

type tenantGuard struct {
	resolve  TenantResolver
	optional bool
	override bool
	realm    string
	logger   func() *slog.Logger
}

func (s *ServerHttp) tenantGuard() *tenantGuard {
	cfg := s.Deps.Config.HTTP
	resolve := TenantFromClaim()
	if cfg.Tenant.Source == "header" {
		resolve = TenantFromHeader(cfg.Tenant.Header)
	}
	realm := cfg.Auth.Realm
	if realm == "" {
		realm = DefaultRealm
	}
	return &tenantGuard{resolve: resolve, realm: realm, logger: func() *slog.Logger { return &s.Deps.Logger }}
}

func (g *tenantGuard) middleware() func(stdhttp.Handler) stdhttp.Handler {
	return func(next stdhttp.Handler) stdhttp.Handler {
		return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
			if r, ok := g.apply(w, r, codec.Default().DefaultFormat()); ok {
				next.ServeHTTP(w, r)
			}
		})
	}
}

// apply resolves and stores the tenant. On failure it has already written
// the response.
func (g *tenantGuard) apply(w stdhttp.ResponseWriter, r *stdhttp.Request, format string) (*stdhttp.Request, bool) {
	ctx := r.Context()
	if _, ok := database.TenantFromContext(ctx); ok && !g.override {
		return r, true
	}
	raw, err := g.resolve(r)
	if errors.Is(err, database.ErrNoTenant) {
		raw, err = "", nil
	}
	switch {
	case errors.Is(err, errNoPrincipal):
		fail(w, r, format, g.realm, stdhttp.StatusUnauthorized, detailAuthRequired, "", nil)
		return nil, false
	case errors.Is(err, database.ErrInvalidTenantID):
		g.reject(w, r, format, stdhttp.StatusBadRequest, detailTenantInvalid, "invalid_tenant")
		return nil, false
	case err != nil:
		g.logger().ErrorContext(ctx, "[HTTPModule] tenant resolution failed", "error", err)
		g.reject(w, r, format, stdhttp.StatusInternalServerError, detailInternal, "")
		return nil, false
	case raw == "":
		if g.optional {
			return r, true
		}
		g.reject(w, r, format, stdhttp.StatusForbidden, detailTenantRequired, "missing_tenant")
		return nil, false
	}
	tenantCtx, err := database.WithTenant(ctx, raw)
	if err != nil {
		g.reject(w, r, format, stdhttp.StatusBadRequest, detailTenantInvalid, "invalid_tenant")
		return nil, false
	}
	return r.WithContext(tenantCtx), true
}

// reject answers without a WWW-Authenticate challenge: the credentials (if
// any) were fine, the tenant was not. The tenant value is never logged.
func (g *tenantGuard) reject(w stdhttp.ResponseWriter, r *stdhttp.Request, format string, status int, detail, reason string) {
	if reason != "" {
		g.logger().DebugContext(r.Context(), "[HTTPModule] tenant rejected", "reason", reason)
	}
	if err := write(w, r, status, format, detail); err != nil {
		_ = write(w, r, stdhttp.StatusInternalServerError, format, detailInternal)
	}
}
