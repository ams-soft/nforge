package nhttp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	stdhttp "net/http"
	"reflect"
	"slices"
	"strings"

	router "github.com/ams-soft/go-ams-router"
	"github.com/ams-soft/nforge/codec"
	"github.com/ams-soft/nforge/http/internal/capture"
	"github.com/ams-soft/nforge/http/internal/query"
)

// Opts configures a typed route. All fields are optional.
type Opts struct {
	// Codec is the request and response format ("json", "xml", "cbor" or
	// the content type). Default: JSON.
	Codec string
	// IsPrivate requires a valid bearer JWT before the request is decoded:
	// the route is authenticated with AuthProfile, or http.auth.default_profile
	// when empty, and documented with the bearer security scheme. It panics at
	// registration when jose is disabled or the profile is not a usable jwt
	// profile.
	IsPrivate bool
	// AuthProfile selects the jose jwt profile of a private route. Setting it
	// without IsPrivate panics, to catch routes left public by mistake.
	AuthProfile string
	// RequiredScopes lists scopes the token must all hold (403 otherwise).
	// Requires IsPrivate.
	RequiredScopes []string
	// Tenant requires a tenant in the request context (see Tenant), resolved
	// after authentication and before decoding from http.tenant: the JWT
	// tenant_id claim (default, requires IsPrivate) or a header. A tenant
	// already stored by a global middleware is kept.
	Tenant bool
	// DocTag groups the route in the documentation.
	DocTag string
	// Headers lists the route's headers, for the documentation.
	Headers []string
	// Description describes the route in the documentation.
	Description string
}

// TypedRoute is the record of a typed route, used by the documentation
// generator. Schema and Response hold a value of the request and response
// types (zero value), or nil when the route has none.
type TypedRoute struct {
	Method      string
	Description string
	Path        string
	Handler     stdhttp.Handler
	// IsPrivate documents the route as protected. For typed routes it is also
	// enforced; for AddDocRoute it is documentation only, since the raw
	// handler must mount Authenticate itself.
	IsPrivate      bool
	AuthProfile    string
	RequiredScopes []string
	// Tenant documents (and, for typed routes, enforces) a required tenant;
	// TenantHeader is the header it is read from, if any.
	Tenant       bool
	TenantHeader string
	Tag          string
	Headers      []string
	Codec        string
	Schema       any
	Response     any
}

// TypedRouter is the application router with typed routes (TGet, TPost,
// TPut, TPatch, TDelete). The router's methods remain available; use the
// TypedRouter's Route, Group and With so that the path registered in the
// documentation includes the prefix.
type TypedRouter struct {
	router.Router
	srv    *ServerHttp
	prefix string
}

// Typed returns the TypedRouter at the server root.
func (s *ServerHttp) Typed() *TypedRouter {
	return &TypedRouter{Router: s.Router(), srv: s}
}

// AddDocRoute registers a non-typed route in the documentation (the
// Handler is ignored). For GET, the Schema fields become the query string.
// IsPrivate here is documentation only: protect the raw handler with
// Authenticate yourself.
func (s *ServerHttp) AddDocRoute(route TypedRoute) {
	route.Method = strings.ToUpper(route.Method)
	route.Codec = mustFormat(route.Codec)
	route.Handler = nil
	route.Schema = zeroOf(typeOf(route.Schema))
	route.Response = zeroOf(typeOf(route.Response))
	s.TypedRoutes = append(s.TypedRoutes, route)
}

// Route mounts a sub-router at pattern, like router.Route, adding the
// prefix to the path of the typed routes registered inside it.
func (tr *TypedRouter) Route(pattern string, fn func(tr *TypedRouter)) *TypedRouter {
	prefix := joinRoute(tr.prefix, pattern)
	sub := tr.Router.Route(pattern, func(r router.Router) {
		fn(&TypedRouter{Router: r, srv: tr.srv, prefix: prefix})
	})
	return &TypedRouter{Router: sub, srv: tr.srv, prefix: prefix}
}

// Group creates a group on the same prefix with its own middleware stack.
func (tr *TypedRouter) Group(fn func(tr *TypedRouter)) *TypedRouter {
	sub := tr.Router.Group(func(r router.Router) {
		fn(&TypedRouter{Router: r, srv: tr.srv, prefix: tr.prefix})
	})
	return &TypedRouter{Router: sub, srv: tr.srv, prefix: tr.prefix}
}

// With returns a TypedRouter with inline middlewares, on the same prefix.
func (tr *TypedRouter) With(middlewares ...func(stdhttp.Handler) stdhttp.Handler) *TypedRouter {
	return &TypedRouter{Router: tr.Router.With(middlewares...), srv: tr.srv, prefix: tr.prefix}
}

// TGet registers a GET whose query string is decoded and validated into query
// (a struct, or nil) before the handler. The handler reads the value with
// Query[T](r) and responds with SendJSON/SendXML/SendCBOR: the status it
// sends is the response status (200 if none), the format is the route's Codec
// and, on success, the message is validated against response (nil disables
// validation).
func (tr *TypedRouter) TGet(pattern string, query, response any, h stdhttp.HandlerFunc, opts ...Opts) {
	tr.handle(stdhttp.MethodGet, pattern, query, response, h, opts)
}

// TPost registers a POST whose body is decoded and validated into request
// (a struct, or nil for a route without body) in the route's Codec before the
// handler. The handler reads the value with Body[T](r); the response follows
// the TGet rules.
func (tr *TypedRouter) TPost(pattern string, request, response any, h stdhttp.HandlerFunc, opts ...Opts) {
	tr.handle(stdhttp.MethodPost, pattern, request, response, h, opts)
}

// TPut is TPost for PUT.
func (tr *TypedRouter) TPut(pattern string, request, response any, h stdhttp.HandlerFunc, opts ...Opts) {
	tr.handle(stdhttp.MethodPut, pattern, request, response, h, opts)
}

// TPatch is TPost for PATCH.
func (tr *TypedRouter) TPatch(pattern string, request, response any, h stdhttp.HandlerFunc, opts ...Opts) {
	tr.handle(stdhttp.MethodPatch, pattern, request, response, h, opts)
}

// TDelete is TPost for DELETE.
func (tr *TypedRouter) TDelete(pattern string, request, response any, h stdhttp.HandlerFunc, opts ...Opts) {
	tr.handle(stdhttp.MethodDelete, pattern, request, response, h, opts)
}

type requestKey struct{}

// Body returns the already decoded and validated body of a typed route. T is
// the type passed as request (or a pointer to it); outside a typed route, or
// with a different T, it returns the zero value.
func Body[T any](r *stdhttp.Request) T {
	var zero T
	ptr := r.Context().Value(requestKey{})
	if ptr == nil {
		return zero
	}
	if v, ok := ptr.(T); ok {
		return v
	}
	if v, ok := reflect.ValueOf(ptr).Elem().Interface().(T); ok {
		return v
	}
	return zero
}

// Query returns the already decoded and validated query string of a TGet.
func Query[T any](r *stdhttp.Request) T { return Body[T](r) }

func (tr *TypedRouter) handle(method, pattern string, request, response any, h stdhttp.HandlerFunc, opts []Opts) {
	var o Opts
	if len(opts) > 0 {
		o = opts[0]
	}
	format := mustFormat(o.Codec)
	reqType, resType := typeOf(request), typeOf(response)
	isQuery := method == stdhttp.MethodGet
	path := joinRoute(tr.prefix, pattern)

	if reqType != nil {
		if isQuery {
			if err := query.Check(reqType); err != nil {
				panic(fmt.Sprintf("nhttp: %s %s: %v", method, path, err))
			}
		} else {
			mustValidTags(method, path, format, reqType)
		}
	}
	if resType != nil {
		mustValidTags(method, path, format, resType)
	}
	auth, profile := tr.srv.routeAuth(method, path, o)
	tenant, tenantHeader := tr.srv.routeTenant(method, path, o)

	handler := tr.wrap(format, reqType, resType, isQuery, auth, tenant, h)
	tr.MethodFunc(method, pattern, handler)
	tr.srv.TypedRoutes = append(tr.srv.TypedRoutes, TypedRoute{
		Method:         method,
		Description:    o.Description,
		Path:           path,
		Handler:        handler,
		IsPrivate:      o.IsPrivate,
		AuthProfile:    profile,
		RequiredScopes: authScopes(auth),
		Tenant:         o.Tenant,
		TenantHeader:   tenantHeader,
		Tag:            o.DocTag,
		Headers:        o.Headers,
		Codec:          format,
		Schema:         zeroOf(reqType),
		Response:       zeroOf(resType),
	})
}

// routeAuth builds the authenticator of a private route, panicking on any
// inconsistency so that a route cannot silently end up public.
func (s *ServerHttp) routeAuth(method, path string, o Opts) (*authenticator, string) {
	fail := func(format string, args ...any) {
		panic(fmt.Sprintf("nhttp: %s %s: ", method, path) + fmt.Sprintf(format, args...))
	}
	if !o.IsPrivate {
		if o.AuthProfile != "" || len(o.RequiredScopes) > 0 {
			fail("AuthProfile and RequiredScopes require IsPrivate: true")
		}
		return nil, ""
	}
	cfg := s.Deps.Config
	if cfg.HTTP.Docs.Enabled && cfg.HTTP.Docs.Auth == "none" {
		fail("private route with http.docs.auth: none would be documented without security")
	}
	profile := o.AuthProfile
	if profile == "" {
		profile = cfg.HTTP.Auth.DefaultProfile
	}
	if profile == "" {
		fail("private route without AuthProfile and without http.auth.default_profile")
	}
	realm := cfg.HTTP.Auth.Realm
	if realm == "" {
		realm = DefaultRealm
	}
	logger := func() *slog.Logger { return &s.Deps.Logger }
	auth, err := newAuthenticator(s.Deps.JOSE, profile, []AuthOption{WithRealm(realm), WithScopes(o.RequiredScopes...)}, logger)
	if err != nil {
		fail("%v", err)
	}
	return auth, profile
}

// routeTenant builds the tenant guard of a route with Opts.Tenant. The claim
// source needs a Principal, so it panics on a public route.
func (s *ServerHttp) routeTenant(method, path string, o Opts) (*tenantGuard, string) {
	if !o.Tenant {
		return nil, ""
	}
	src := s.Deps.Config.HTTP.Tenant
	if src.Source == "header" {
		return s.tenantGuard(), src.Header
	}
	if !o.IsPrivate {
		panic(fmt.Sprintf("nhttp: %s %s: Tenant with http.tenant.source: claim requires IsPrivate: true", method, path))
	}
	return s.tenantGuard(), ""
}

func authScopes(a *authenticator) []string {
	if a == nil {
		return nil
	}
	return slices.Clone(a.scopes)
}

func (tr *TypedRouter) wrap(format string, reqType, resType reflect.Type, isQuery bool, auth *authenticator, tenant *tenantGuard, h stdhttp.HandlerFunc) stdhttp.HandlerFunc {
	logger := &tr.srv.Deps.Logger
	strict := tr.srv.Deps.Config.Codec.StrictUnknownFields
	return func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		if auth != nil {
			// authentication runs before decoding: unauthenticated clients
			// learn nothing about the request schema
			var ok bool
			if r, ok = auth.authenticate(w, r, format); !ok {
				return
			}
		}
		if tenant != nil {
			var ok bool
			if r, ok = tenant.apply(w, r, format); !ok {
				return
			}
		}
		if reqType != nil {
			ptr, err := decodeRequest(r, format, reqType, isQuery, strict)
			if err != nil {
				logger.WarnContext(r.Context(), "invalid request", "error", err)
				if err := write(w, r, ParseStatus(err), format, err.Error()); err != nil {
					logger.ErrorContext(r.Context(), "failed to send response", "error", err)
				}
				return
			}
			r = r.WithContext(context.WithValue(r.Context(), requestKey{}, ptr))
		}

		cw := capture.NewWriter(w)
		h(cw, r)

		status, message, err := cw.Result(format, resType)
		if err != nil {
			logger.ErrorContext(r.Context(), "invalid handler response", "error", err)
			status, message = stdhttp.StatusInternalServerError, "failed to build the response"
		}
		if err := write(w, r, status, format, message); err != nil {
			logger.ErrorContext(r.Context(), "failed to send response", "error", err)
			_ = write(w, r, stdhttp.StatusInternalServerError, format, "failed to build the response")
		}
	}
}

func decodeRequest(r *stdhttp.Request, format string, t reflect.Type, isQuery, strict bool) (any, error) {
	if isQuery {
		ptr, err := query.Decode(r.URL.Query(), t, strict)
		if err != nil {
			return nil, err
		}
		if err := codec.Default().Validate(ptr.Interface()); err != nil {
			return nil, err
		}
		return ptr.Interface(), nil
	}
	ptr := reflect.New(t).Interface()
	if err := parse(r, format, ptr); err != nil {
		return nil, err
	}
	return ptr, nil
}

// mustFormat resolves the route's format (default JSON) in the Registry,
// panicking at registration if it is not enabled.
func mustFormat(format string) string {
	if format == "" {
		format = codec.JSON
	}
	c, err := codec.Default().Lookup(format)
	if err != nil {
		panic(fmt.Sprintf("nhttp: route codec: %v", err))
	}
	return c.ContentType()
}

// mustValidTags moves to route registration the struct tag errors that the
// codec would only report on the first request.
func mustValidTags(method, path, format string, t reflect.Type) {
	_, err := codec.Default().EncodeAs(format, reflect.New(t).Interface())
	if errors.Is(err, codec.ErrInvalidTags) {
		panic(fmt.Sprintf("nhttp: %s %s: %v", method, path, err))
	}
}

func typeOf(v any) reflect.Type {
	t := reflect.TypeOf(v)
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

func zeroOf(t reflect.Type) any {
	if t == nil {
		return nil
	}
	return reflect.Zero(t).Interface()
}

func joinRoute(prefix, pattern string) string {
	if prefix == "" {
		return pattern
	}
	if pattern == "" || pattern == "/" {
		return prefix
	}
	return strings.TrimSuffix(prefix, "/") + "/" + strings.TrimPrefix(pattern, "/")
}
