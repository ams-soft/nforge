package main

import (
	"crypto/subtle"
	stdhttp "net/http"
	"time"

	"github.com/ams-soft/nforge/deps"
	nhttp "github.com/ams-soft/nforge/http"
	"github.com/ams-soft/nforge/jose"
)

// Demo credentials. DO NOT ship hard-coded credentials: a real application
// checks the user store (with hashed passwords) before issuing a token.
const (
	demoEmail    = "admin@admin.com"
	demoPassword = "admin123"
)

// LoginRequest is the body of POST /auth/login.
type LoginRequest struct {
	Email    string `json:"email" codec:"required"`
	Password string `json:"password" codec:"required"`
}

// LoginResponse follows the OAuth 2.0 token response field names.
type LoginResponse struct {
	AccessToken string `json:"access_token" codec:"required"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int64  `json:"expires_in"`
}

// MeResponse exposes the safe fields of the authenticated principal.
type MeResponse struct {
	Subject  string   `json:"sub"`
	Issuer   string   `json:"iss"`
	Audience []string `json:"aud"`
	TokenID  string   `json:"jti"`
}

// registerAuthRoutes registers the demo login and one private route. The
// token profile is http.auth.default_profile ("access" in config.yaml); see
// the jose example for scopes, nested tokens and raw Authenticate.
func registerAuthRoutes(server *nhttp.ServerHttp) {
	dps := server.Deps
	logger := &server.Deps.Logger
	const profile = "access"

	server.Typed().Route("/auth", func(auth *nhttp.TypedRouter) {
		auth.TPost("/login", LoginRequest{}, LoginResponse{}, func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
			in := nhttp.Body[LoginRequest](r)
			// Constant-time comparisons; both run so timing does not reveal which one failed.
			emailOK := subtle.ConstantTimeCompare([]byte(in.Email), []byte(demoEmail)) == 1
			passOK := subtle.ConstantTimeCompare([]byte(in.Password), []byte(demoPassword)) == 1
			if !emailOK || !passOK {
				_ = nhttp.SendJSON(w, r, stdhttp.StatusUnauthorized, "invalid credentials")
				return
			}
			token, err := dps.JOSE.IssueJWT(r.Context(), profile, jose.RegisteredClaims{Subject: "1"}, nil)
			if err != nil {
				logger.ErrorContext(r.Context(), "failed to issue token", "error", err) // sanitized: no token or key data
				_ = nhttp.SendJSON(w, r, stdhttp.StatusInternalServerError, "internal error")
				return
			}
			_ = nhttp.SendJSON(w, r, stdhttp.StatusOK, LoginResponse{
				AccessToken: token, TokenType: "Bearer", ExpiresIn: tokenTTL(dps, profile),
			})
		}, nhttp.Opts{DocTag: "auth", Description: "Demo login (admin@admin.com / admin123): issues a bearer token"})

		// IsPrivate is enforced: the handler only runs with a valid token.
		auth.TGet("/me", nil, MeResponse{}, func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
			p, _ := nhttp.PrincipalFromContext(r.Context())
			_ = nhttp.SendJSON(w, r, stdhttp.StatusOK, MeResponse{
				Subject: p.Subject, Issuer: p.Issuer, Audience: p.Audience, TokenID: p.TokenID,
			})
		}, nhttp.Opts{DocTag: "auth", Description: "Returns the authenticated principal", IsPrivate: true})
	})
}

func tokenTTL(dps deps.Deps, profile string) int64 {
	for _, p := range dps.Config.JOSE.Profiles {
		if p.Name == profile {
			return int64(p.JWT.TTL / time.Second)
		}
	}
	return 0
}
