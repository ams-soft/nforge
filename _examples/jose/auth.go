package main

import (
	"context"
	"crypto/subtle"
	stdhttp "net/http"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ams-soft/nforge/deps"
	nhttp "github.com/ams-soft/nforge/http"
	"github.com/ams-soft/nforge/jose"
)

// demoScopes are the scopes granted by the demo login.
var demoScopes = []string{"wallet:read", "wallet:write", "users:write"}

// demoTenant is the tenant_id claim of the demo tokens. nhttp.Tenant() and
// Opts.Tenant read it from the Principal (see the database example for
// tenant-scoped data).
const demoTenant = "6f1c3e2a-8b4d-4f7e-9a1b-2c3d4e5f6a7b"

// protectedCalls counts how many times a protected handler ran; main_test.go
// uses it to prove handlers never run after an authentication failure.
var protectedCalls atomic.Int64

// LoginRequest is the body of POST /auth/login.
type LoginRequest struct {
	Username string `json:"username" codec:"required"`
	Password string `json:"password" codec:"required"`
	// Profile is the token kind: "access" (signed JWT, default) or
	// "confidential" (nested JWT: signed, then encrypted).
	Profile string `json:"profile,omitempty"`
	// Scope optionally narrows the granted scopes (space-separated subset of demoScopes).
	Scope string `json:"scope,omitempty"`
}

// LoginResponse follows the OAuth 2.0 token response field names.
type LoginResponse struct {
	AccessToken string `json:"access_token" codec:"required"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int64  `json:"expires_in"`
	Scope       string `json:"scope,omitempty"`
}

// AccessClaims are the custom claims of the example tokens. The default
// nhttp principal mapper reads tenant_id, roles and scope.
type AccessClaims struct {
	TenantID string   `json:"tenant_id"`
	Roles    []string `json:"roles,omitempty"`
	Scope    string   `json:"scope,omitempty"`
}

// MeResponse exposes the safe fields of the authenticated principal.
type MeResponse struct {
	Subject  string   `json:"sub"`
	Issuer   string   `json:"iss"`
	Audience []string `json:"aud"`
	TokenID  string   `json:"jti"`
	TenantID string   `json:"tenant_id,omitempty"`
	Roles    []string `json:"roles,omitempty"`
	Scopes   []string `json:"scopes,omitempty"`
}

// Wallet is a fake resource protected by scopes.
type Wallet struct {
	ID      string `json:"id" codec:"required"`
	Owner   string `json:"owner"`
	Balance int64  `json:"balance"`
}

// JOSEDemoRequest is the body of the generic JWS/JWE demo.
type JOSEDemoRequest struct {
	Message string `json:"message" codec:"required"`
}

// JOSEDemoResponse shows the compact JWS and JWE and their round trip.
type JOSEDemoResponse struct {
	JWS       string `json:"jws"`
	JWE       string `json:"jwe"`
	Verified  string `json:"verified"`
	Decrypted string `json:"decrypted"`
}

func meFrom(p nhttp.Principal) MeResponse {
	return MeResponse{Subject: p.Subject, Issuer: p.Issuer, Audience: p.Audience, TokenID: p.TokenID,
		TenantID: p.TenantID, Roles: p.Roles, Scopes: p.Scopes}
}

// listWallets is shared by every protected wallet route. Handlers read the
// identity with PrincipalFromContext; the tenant comes from the validated
// token, never from a header.
func listWallets(w stdhttp.ResponseWriter, req *stdhttp.Request) {
	protectedCalls.Add(1)
	p, ok := nhttp.PrincipalFromContext(req.Context())
	if !ok { // unreachable behind Authenticate; kept as defense in depth
		_ = nhttp.SendJSON(w, req, stdhttp.StatusUnauthorized, "authentication required")
		return
	}
	// A real application checks tenant membership and resource ownership here.
	_ = nhttp.SendJSON(w, req, stdhttp.StatusOK, []Wallet{{ID: "w-1", Owner: p.Subject, Balance: 1500}})
}

// registerRoutes registers the login, the protected demo routes and the
// optional JWS/JWE demo.
func registerRoutes(server *nhttp.ServerHttp) {
	dps := server.Deps
	logger := &server.Deps.Logger
	r := server.Router()

	// Public route: no authentication.
	// curl localhost:8080/public
	r.Get("/public", func(w stdhttp.ResponseWriter, req *stdhttp.Request) {
		_ = nhttp.SendJSON(w, req, stdhttp.StatusOK, "public content")
	})

	// Raw route protected by a single middleware that validates the token and
	// requires BOTH scopes (403 when one is missing).
	// curl localhost:8080/raw/wallets -H "Authorization: Bearer $TOKEN"
	r.With(nhttp.Authenticate(dps.JOSE, "access",
		nhttp.WithScopes("wallet:read", "wallet:write"),
	)).Get("/raw/wallets", listWallets)

	// The same requirement built from a slice: WithScopes(requiredScopes...)
	// behaves exactly like the variadic form above.
	requiredScopes := []string{"wallet:read", "wallet:write"}
	r.With(nhttp.Authenticate(dps.JOSE, "access", nhttp.WithScopes(requiredScopes...))).Get("/raw/wallets-slice", listWallets)

	// AddDocRoute only documents raw routes: the protection above is what enforces it.
	server.AddDocRoute(nhttp.TypedRoute{Method: "GET", Path: "/raw/wallets", Tag: "auth", IsPrivate: true,
		RequiredScopes: requiredScopes, Description: "Lists wallets (raw route with Authenticate)", Response: []Wallet{}})

	server.Typed().Group(func(root *nhttp.TypedRouter) {
		// Demo login. DO NOT ship hard-coded credentials: this only shows how a
		// token is issued after the application authenticated the user.
		// curl -X POST localhost:8080/auth/login -H 'Content-Type: application/json' --data-binary '{"username":"admin","password":"admin"}'
		root.TPost("/auth/login", LoginRequest{}, LoginResponse{}, func(w stdhttp.ResponseWriter, req *stdhttp.Request) {
			in := nhttp.Body[LoginRequest](req)

			// With a database, the check would be something like:
			//
			//	query, args, err := db.Query.Select("id", "password_hash", "tenant_id").
			//		From("accounts").Where(sq.Eq{"username": in.Username, "active": true}).ToSql()
			//	var id int64; var hash []byte; var tenant string
			//	err = db.Executor.QueryRow(ctx, query, args...).Scan(&id, &hash, &tenant)
			//	if errors.Is(err, pgx.ErrNoRows) || bcrypt.CompareHashAndPassword(hash, []byte(in.Password)) != nil {
			//		// same 401 for unknown user and wrong password
			//	}
			//	subject := strconv.FormatInt(id, 10)
			userOK := subtle.ConstantTimeCompare([]byte(in.Username), []byte("admin")) == 1
			passOK := subtle.ConstantTimeCompare([]byte(in.Password), []byte("admin")) == 1
			if !userOK || !passOK {
				_ = nhttp.SendJSON(w, req, stdhttp.StatusUnauthorized, "invalid credentials")
				return
			}

			profile := in.Profile
			if profile == "" {
				profile = "access"
			}
			if profile != "access" && profile != "confidential" {
				_ = nhttp.SendJSON(w, req, stdhttp.StatusBadRequest, "profile must be access or confidential")
				return
			}
			scopes := demoScopes
			if in.Scope != "" {
				scopes = strings.Fields(in.Scope)
				for _, s := range scopes {
					if !slices.Contains(demoScopes, s) {
						_ = nhttp.SendJSON(w, req, stdhttp.StatusBadRequest, "scope must be a subset of: "+strings.Join(demoScopes, " "))
						return
					}
				}
			}
			scope := strings.Join(scopes, " ")

			token, err := dps.JOSE.IssueJWT(req.Context(), profile,
				jose.RegisteredClaims{Subject: "user-1"}, // the user id from the database
				AccessClaims{TenantID: demoTenant, Roles: []string{"operator"}, Scope: scope})
			if err != nil {
				logger.ErrorContext(req.Context(), "failed to issue token", "error", err) // sanitized: no token or key data
				_ = nhttp.SendJSON(w, req, stdhttp.StatusInternalServerError, "internal error")
				return
			}
			_ = nhttp.SendJSON(w, req, stdhttp.StatusOK, LoginResponse{
				AccessToken: token, TokenType: "Bearer", ExpiresIn: tokenTTL(dps, profile), Scope: scope,
			})
		}, nhttp.Opts{DocTag: "auth", Description: "Demo login (admin/admin): issues an access or confidential token"})

		// Typed private route using the default profile (http.auth.default_profile).
		// curl localhost:8080/me -H "Authorization: Bearer $TOKEN"
		root.TGet("/me", nil, MeResponse{}, func(w stdhttp.ResponseWriter, req *stdhttp.Request) {
			protectedCalls.Add(1)
			p, _ := nhttp.PrincipalFromContext(req.Context())
			_ = nhttp.SendJSON(w, req, stdhttp.StatusOK, meFrom(p))
		}, nhttp.Opts{DocTag: "auth", Description: "Returns the authenticated principal", IsPrivate: true})
	})

	server.Typed().Route("/api/v1", func(api *nhttp.TypedRouter) {
		// Typed private route with an explicit profile and a required scope.
		// curl localhost:8080/api/v1/wallets -H "Authorization: Bearer $TOKEN"
		api.TGet("/wallets", nil, []Wallet{}, listWallets, nhttp.Opts{
			DocTag: "wallets", Description: "Lists wallets",
			IsPrivate: true, AuthProfile: "access", RequiredScopes: []string{"wallet:read"},
		})

		// Only nested (signed + encrypted) tokens of the confidential profile pass here;
		// access tokens get 401.
		// curl localhost:8080/api/v1/confidential -H "Authorization: Bearer $CONFIDENTIAL_TOKEN"
		api.TGet("/confidential", nil, MeResponse{}, func(w stdhttp.ResponseWriter, req *stdhttp.Request) {
			protectedCalls.Add(1)
			p, _ := nhttp.PrincipalFromContext(req.Context())
			_ = nhttp.SendJSON(w, req, stdhttp.StatusOK, meFrom(p))
		}, nhttp.Opts{DocTag: "wallets", Description: "Requires a confidential (nested) token", IsPrivate: true, AuthProfile: "confidential"})

		// Optional generic JWS/JWE round trip; only registered when the
		// message-jws and message-jwe profiles exist in config.yaml.
		_, hasJWS := dps.JOSE.Profile("message-jws")
		_, hasJWE := dps.JOSE.Profile("message-jwe")
		if hasJWS && hasJWE {
			// curl -X POST localhost:8080/api/v1/jose/demo -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' --data-binary '{"message":"hello"}'
			api.TPost("/jose/demo", JOSEDemoRequest{}, JOSEDemoResponse{}, func(w stdhttp.ResponseWriter, req *stdhttp.Request) {
				resp, err := joseRoundTrip(req.Context(), dps.JOSE, []byte(nhttp.Body[JOSEDemoRequest](req).Message))
				if err != nil {
					logger.ErrorContext(req.Context(), "jose demo failed", "error", err)
					_ = nhttp.SendJSON(w, req, stdhttp.StatusInternalServerError, "internal error")
					return
				}
				_ = nhttp.SendJSON(w, req, stdhttp.StatusOK, resp)
			}, nhttp.Opts{DocTag: "jose", Description: "Signs (JWS) and encrypts (JWE) a message, then verifies and decrypts it", IsPrivate: true})
		}
	})
}

// joseRoundTrip signs and encrypts msg with the generic profiles, then
// verifies and decrypts the results.
func joseRoundTrip(ctx context.Context, svc *jose.Service, msg []byte) (JOSEDemoResponse, error) {
	jws, err := svc.Sign(ctx, "message-jws", msg)
	if err != nil {
		return JOSEDemoResponse{}, err
	}
	jwe, err := svc.Encrypt(ctx, "message-jwe", msg)
	if err != nil {
		return JOSEDemoResponse{}, err
	}
	verified, err := svc.Verify(ctx, "message-jws", jws)
	if err != nil {
		return JOSEDemoResponse{}, err
	}
	decrypted, err := svc.Decrypt(ctx, "message-jwe", jwe)
	if err != nil {
		return JOSEDemoResponse{}, err
	}
	return JOSEDemoResponse{JWS: jws, JWE: jwe, Verified: string(verified.Payload), Decrypted: string(decrypted.Plaintext)}, nil
}

// tokenTTL reads the configured lifetime of a jwt profile, in seconds.
func tokenTTL(dps deps.Deps, profile string) int64 {
	for _, p := range dps.Config.JOSE.Profiles {
		if p.Name == profile {
			return int64(p.JWT.TTL / time.Second)
		}
	}
	return 0
}
