package nhttp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	stdhttp "net/http"
	"slices"
	"strings"

	"github.com/ams-soft/nforge/codec"
	"github.com/ams-soft/nforge/http/internal/authn"
	"github.com/ams-soft/nforge/jose"
)

// DefaultRealm is the WWW-Authenticate realm used when none is configured.
const DefaultRealm = "nforge-api"

// Static, generic response details: never upstream errors or token data.
const (
	detailAuthRequired   = "authentication required"
	detailInvalidRequest = "invalid authentication request"
	detailInvalidToken   = "invalid token"
	detailForbidden      = "insufficient permissions"
	detailUnavailable    = "authentication unavailable"
	detailInternal       = "internal error"
)

// Principal is the identity of an authenticated request. Issuer, Subject,
// Audience and TokenID always come from the validated registered claims;
// TenantID, Roles and Scopes come from the PrincipalMapper (by default the
// custom claims tenant_id, roles and scope). Roles grant nothing by
// themselves, and TenantID never comes from request headers: tenant-sensitive
// applications must still check membership and resource authorization.
type Principal struct {
	Subject  string
	Issuer   string
	Audience []string
	TokenID  string
	TenantID string
	Roles    []string
	Scopes   []string
}

// HasScopes reports whether the principal holds every scope (exact,
// case-sensitive match).
func (p Principal) HasScopes(scopes ...string) bool {
	for _, s := range scopes {
		if !slices.Contains(p.Scopes, s) {
			return false
		}
	}
	return true
}

func (p Principal) clone() Principal {
	p.Audience = slices.Clone(p.Audience)
	p.Roles = slices.Clone(p.Roles)
	p.Scopes = slices.Clone(p.Scopes)
	return p
}

type principalKey struct{}

// PrincipalFromContext returns the identity stored by Authenticate (or a
// private typed route). The slices are copies: changing them does not affect
// other readers. The raw bearer token is never stored.
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(*Principal)
	if !ok || p == nil {
		return Principal{}, false
	}
	return p.clone(), true
}

func withPrincipal(ctx context.Context, p Principal) context.Context {
	c := p.clone()
	return context.WithValue(ctx, principalKey{}, &c)
}

// PrincipalMapper builds the application identity from the validated
// claims. custom is the normalized map of custom claims (numbers as
// json.Number). Return an error matching jose.ErrClaimsRejected to reject
// the token (401) or jose.ErrValidationUnavailable when a dependency is down
// (503); any other error is an internal failure (500). Issuer, Subject,
// Audience and TokenID are always overwritten from the registered claims.
type PrincipalMapper func(ctx context.Context, registered jose.RegisteredClaims, custom any) (Principal, error)

// AuthOption configures Authenticate.
type AuthOption func(*authOptions)

type authOptions struct {
	realm  string
	scopes []string
	mapper PrincipalMapper
}

// WithRealm sets the WWW-Authenticate realm (default "nforge-api"). It
// must be printable ASCII without quotes or backslashes.
func WithRealm(realm string) AuthOption {
	return func(o *authOptions) { o.realm = realm }
}

// WithScopes requires every listed scope (exact, case-sensitive) after the
// token is validated; a missing scope is a 403. Accepts individual scopes or
// a slice expansion (WithScopes(required...)); the values are copied, and
// several WithScopes options accumulate.
func WithScopes(scopes ...string) AuthOption {
	copied := slices.Clone(scopes)
	return func(o *authOptions) { o.scopes = append(o.scopes, copied...) }
}

// WithPrincipalMapper replaces the default mapping of the custom claims
// tenant_id (string), roles (array of strings) and scope (space-separated
// string).
func WithPrincipalMapper(mapper PrincipalMapper) AuthOption {
	return func(o *authOptions) { o.mapper = mapper }
}

// authenticator is the shared implementation of Authenticate and of private
// typed routes.
type authenticator struct {
	service *jose.Service
	profile string
	realm   string
	scopes  []string
	mapper  PrincipalMapper
	logger  func() *slog.Logger
}

// Authenticate returns a middleware that requires a valid bearer JWT of the
// given jwt_signed or jwt_nested profile and stores the resulting Principal
// in the request context. Failures answer with the standard envelope in the
// default codec and a WWW-Authenticate challenge: 401 for missing or invalid
// tokens, 400 for malformed credential transport, 403 for missing scopes,
// 503 when validation is unavailable. Invalid arguments panic, so mistakes
// surface at startup.
func Authenticate(service *jose.Service, profile string, opts ...AuthOption) func(stdhttp.Handler) stdhttp.Handler {
	a, err := newAuthenticator(service, profile, opts, slog.Default)
	if err != nil {
		panic("nhttp: Authenticate: " + err.Error())
	}
	return func(next stdhttp.Handler) stdhttp.Handler {
		return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
			r, ok := a.authenticate(w, r, codec.Default().DefaultFormat())
			if ok {
				next.ServeHTTP(w, r)
			}
		})
	}
}

// RequireScopes returns a middleware that requires every listed scope from
// the Principal stored by a previous Authenticate. Without a Principal it
// answers 401; with missing scopes, 403. It shares the scope check with
// WithScopes and Opts.RequiredScopes.
func RequireScopes(scopes ...string) func(stdhttp.Handler) stdhttp.Handler {
	required, err := authn.NormalizeScopes(scopes)
	if err != nil {
		panic("nhttp: RequireScopes: " + err.Error())
	}
	return func(next stdhttp.Handler) stdhttp.Handler {
		return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
			format := codec.Default().DefaultFormat()
			p, ok := PrincipalFromContext(r.Context())
			if !ok {
				fail(w, r, format, DefaultRealm, stdhttp.StatusUnauthorized, detailAuthRequired, "", nil)
				return
			}
			if !p.HasScopes(required...) {
				fail(w, r, format, DefaultRealm, stdhttp.StatusForbidden, detailForbidden, "insufficient_scope", required)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func newAuthenticator(service *jose.Service, profile string, opts []AuthOption, logger func() *slog.Logger) (*authenticator, error) {
	if service == nil {
		return nil, errors.New("jose service is nil (jose.enabled: false?)")
	}
	info, ok := service.Profile(profile)
	if !ok {
		return nil, fmt.Errorf("unknown jose profile %q", profile)
	}
	if info.Kind != jose.KindJWTSigned && info.Kind != jose.KindJWTNested {
		return nil, fmt.Errorf("jose profile %q is %s, not a jwt profile", profile, info.Kind)
	}
	if !info.CanConsume {
		return nil, fmt.Errorf("jose profile %q cannot validate tokens", profile)
	}
	o := &authOptions{realm: DefaultRealm}
	for _, opt := range opts {
		if opt == nil {
			return nil, errors.New("nil option")
		}
		opt(o)
	}
	if !authn.ValidRealm(o.realm) {
		return nil, errors.New("realm must be printable ASCII without quotes or backslashes")
	}
	scopes, err := authn.NormalizeScopes(o.scopes)
	if err != nil {
		return nil, err
	}
	mapper := o.mapper
	if mapper == nil {
		mapper = defaultPrincipalMapper
	}
	return &authenticator{service: service, profile: profile, realm: o.realm, scopes: scopes, mapper: mapper, logger: logger}, nil
}

// authenticate validates the credential, maps and checks the principal and
// returns the request carrying it. On failure it has already written the
// response.
func (a *authenticator) authenticate(w stdhttp.ResponseWriter, r *stdhttp.Request, format string) (*stdhttp.Request, bool) {
	ctx := r.Context()
	if _, exists := PrincipalFromContext(ctx); exists {
		a.logger().ErrorContext(ctx, "[HTTPModule] authentication applied twice", "profile", a.profile)
		fail(w, r, format, a.realm, stdhttp.StatusInternalServerError, detailInternal, "", nil)
		return nil, false
	}

	token, status := authn.BearerToken(r, a.service.MaxTokenBytes())
	switch status {
	case stdhttp.StatusUnauthorized:
		fail(w, r, format, a.realm, status, detailAuthRequired, "", nil)
		return nil, false
	case stdhttp.StatusBadRequest:
		a.logger().DebugContext(ctx, "[HTTPModule] authentication failed", "reason", "invalid_request")
		fail(w, r, format, a.realm, status, detailInvalidRequest, "invalid_request", nil)
		return nil, false
	case stdhttp.StatusRequestEntityTooLarge:
		a.logger().DebugContext(ctx, "[HTTPModule] authentication failed", "reason", "token_too_large")
		fail(w, r, format, a.realm, stdhttp.StatusUnauthorized, detailInvalidToken, "invalid_token", nil)
		return nil, false
	}

	var custom map[string]any
	validated, err := a.service.ValidateJWT(ctx, a.profile, token, &custom)
	if err != nil {
		a.failValidation(w, r, format, err)
		return nil, false
	}
	p, err := a.mapper(ctx, validated.Registered, custom)
	if err == nil {
		err = checkPrincipal(&p)
	}
	if err != nil {
		a.failValidation(w, r, format, err)
		return nil, false
	}
	reg := validated.Registered
	p.Issuer, p.Subject, p.Audience, p.TokenID = reg.Issuer, reg.Subject, slices.Clone(reg.Audience), reg.ID

	if !p.HasScopes(a.scopes...) {
		a.logger().DebugContext(ctx, "[HTTPModule] authorization failed", "reason", "insufficient_scope", "profile", a.profile)
		fail(w, r, format, a.realm, stdhttp.StatusForbidden, detailForbidden, "insufficient_scope", a.scopes)
		return nil, false
	}
	return r.WithContext(withPrincipal(ctx, p)), true
}

// failValidation classifies a validation or mapping error without exposing
// it to the client.
func (a *authenticator) failValidation(w stdhttp.ResponseWriter, r *stdhttp.Request, format string, err error) {
	ctx := r.Context()
	reason := "invalid_token"
	var je *jose.Error
	if errors.As(err, &je) {
		reason = je.Code
	}
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded),
		errors.Is(err, jose.ErrClosed), errors.Is(err, jose.ErrValidationUnavailable):
		a.logger().WarnContext(ctx, "[HTTPModule] authentication unavailable", "reason", reason, "profile", a.profile)
		fail(w, r, format, a.realm, stdhttp.StatusServiceUnavailable, detailUnavailable, "", nil)
	case errors.Is(err, jose.ErrClaimsRejected) || (je != nil && !errors.Is(err, jose.ErrInternal)):
		a.logger().DebugContext(ctx, "[HTTPModule] authentication failed", "reason", reason, "profile", a.profile)
		fail(w, r, format, a.realm, stdhttp.StatusUnauthorized, detailInvalidToken, "invalid_token", nil)
	default:
		a.logger().ErrorContext(ctx, "[HTTPModule] authentication internal failure", "reason", reason, "profile", a.profile)
		fail(w, r, format, a.realm, stdhttp.StatusInternalServerError, detailInternal, "", nil)
	}
}

// fail writes the envelope with a sanitized Bearer challenge.
func fail(w stdhttp.ResponseWriter, r *stdhttp.Request, format, realm string, status int, detail, errCode string, scopes []string) {
	if status != stdhttp.StatusServiceUnavailable && status != stdhttp.StatusInternalServerError {
		w.Header().Set("WWW-Authenticate", authn.Challenge(realm, errCode, scopes))
	}
	if err := write(w, r, status, format, detail); err != nil {
		_ = write(w, r, stdhttp.StatusInternalServerError, format, detailInternal)
	}
}

// defaultPrincipalMapper reads the optional custom claims tenant_id
// (string), roles (array of strings) and scope (space-separated string).
// Wrong types reject the token.
func defaultPrincipalMapper(_ context.Context, _ jose.RegisteredClaims, custom any) (Principal, error) {
	var p Principal
	m, _ := custom.(map[string]any)
	reject := func(field string) error {
		return fmt.Errorf("%w: claim %s has the wrong type", jose.ErrClaimsRejected, field)
	}
	if v, ok := m["tenant_id"]; ok {
		s, ok := v.(string)
		if !ok {
			return p, reject("tenant_id")
		}
		p.TenantID = s
	}
	if v, ok := m["roles"]; ok {
		list, ok := v.([]any)
		if !ok {
			return p, reject("roles")
		}
		for _, item := range list {
			s, ok := item.(string)
			if !ok {
				return p, reject("roles")
			}
			p.Roles = append(p.Roles, s)
		}
	}
	if v, ok := m["scope"]; ok {
		s, ok := v.(string)
		if !ok {
			return p, reject("scope")
		}
		for scope := range strings.SplitSeq(s, " ") {
			if scope != "" {
				p.Scopes = append(p.Scopes, scope)
			}
		}
	}
	return p, nil
}

// checkPrincipal validates mapper output before it reaches the context.
func checkPrincipal(p *Principal) error {
	scopes, err := authn.NormalizeScopes(p.Scopes)
	if err != nil {
		return fmt.Errorf("%w: %v", jose.ErrClaimsRejected, err) //nolint:errorlint // only ErrClaimsRejected is part of the contract
	}
	p.Scopes = scopes
	if authn.HasControl(p.TenantID) {
		return fmt.Errorf("%w: invalid tenant", jose.ErrClaimsRejected)
	}
	for _, role := range p.Roles {
		if role == "" || authn.HasControl(role) {
			return fmt.Errorf("%w: invalid role", jose.ErrClaimsRejected)
		}
	}
	p.Roles = slices.Clone(p.Roles)
	return nil
}
