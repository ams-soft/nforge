// Command sse demonstrates Server-Sent Events with nForge: authenticated,
// user-targeted delivery over the memory or the RabbitMQ driver.
//
// Run from the repository root with:
//
//	go run ./_examples/sse -config _examples/sse/config.yaml            # memory driver
//	go run ./_examples/sse -config _examples/sse/config.rabbitmq.yaml   # RabbitMQ driver (see README)
//
// then open http://127.0.0.1:8080/ in two tabs.
package main

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/ams-soft/nforge/app"
	"github.com/ams-soft/nforge/codec"
	"github.com/ams-soft/nforge/config"
	nhttp "github.com/ams-soft/nforge/http"
	"github.com/ams-soft/nforge/jose"
	"github.com/ams-soft/nforge/logs"
	"github.com/ams-soft/nforge/message"
	"github.com/ams-soft/nforge/module"
	"github.com/ams-soft/nforge/sse"
)

//go:embed web/index.html
var indexHTML []byte

func main() {
	cfg, err := config.Load() // parses -config and -envfile
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	a, err := newApp(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := a.Run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// newApp builds the application; the tests reuse it. message.Module is always
// registered: it stays disabled (and sse needs it only for driver: rabbitmq)
// unless the configuration enables it.
func newApp(cfg *config.Document, opts ...app.Option) (*app.Application, error) {
	mods := app.Modules(logs.Module(), codec.Module(), jose.Module(), message.Module(), nhttp.Module(), sse.Module(), notificationsModule())
	return app.New(cfg, append([]app.Option{mods}, opts...)...)
}

const cookieName = "sse_token"

type loginRequest struct {
	User string `json:"user" codec:"required"`
}

type notifyRequest struct {
	User    string `json:"user" codec:"required"`
	Message string `json:"message" codec:"required"`
}

func notificationsModule() module.Descriptor {
	return module.New("notifications", module.Definition{
		Requires: []module.Handle{nhttp.Capability, sse.Capability, jose.Capability},
		Init: func(_ context.Context, sc *module.Scope) (module.Hooks, error) {
			server := module.Require(sc, nhttp.Capability)
			manager := module.Require(sc, sse.Capability)
			tokens := module.Require(sc, jose.Capability)
			r := server.Router()

			r.Get("/", func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				_, _ = w.Write(indexHTML)
			})

			// Demo login: any user name gets a short-lived JWT in an HttpOnly,
			// SameSite=Strict cookie. A real application verifies credentials here.
			r.Post("/login", func(w http.ResponseWriter, req *http.Request) {
				var in loginRequest
				if err := nhttp.ParseJSON(req, &in); err != nil {
					_ = nhttp.SendJSON(w, req, nhttp.ParseStatus(err), err.Error())
					return
				}
				token, err := tokens.IssueJWT(req.Context(), "access", jose.RegisteredClaims{Subject: in.User}, nil)
				if err != nil {
					_ = nhttp.SendJSON(w, req, http.StatusBadRequest, "cannot issue a token")
					return
				}
				http.SetCookie(w, &http.Cookie{ //nolint:gosec // Secure follows the transport: this demo also runs on plain local HTTP
					Name: cookieName, Value: token, Path: "/", MaxAge: 15 * 60,
					HttpOnly: true, SameSite: http.SameSiteStrictMode,
					Secure: req.TLS != nil, // behind HTTPS (or a TLS proxy) always set Secure
				})
				_ = nhttp.SendJSON(w, req, http.StatusOK, map[string]string{"user": in.User})
			})

			// The stream. EventSource cannot send an Authorization header, so the
			// HttpOnly cookie is turned into one for nforge's own bearer
			// authentication, which validates the JWT; the SSE handler's identity is
			// the validated subject, never anything the client chose. The handler
			// also rejects a cross-origin request (Origin check), and SameSite=Strict
			// keeps the cookie off cross-site requests.
			stream := manager.Handler()
			r.With(cookieToBearer, nhttp.Authenticate(tokens, "access")).Get("/notifications/stream", stream.ServeHTTP)

			// Publishing: any authenticated user may notify any user. Real
			// applications publish from their domain code instead.
			r.With(cookieToBearer, nhttp.Authenticate(tokens, "access")).Post("/notify", func(w http.ResponseWriter, req *http.Request) {
				var in notifyRequest
				if err := nhttp.ParseJSON(req, &in); err != nil {
					_ = nhttp.SendJSON(w, req, nhttp.ParseStatus(err), err.Error())
					return
				}
				// The stream key is issuer + tenant + subject: build the target's from the
				// caller's principal (same issuer and tenant), never from client input alone.
				caller, _ := nhttp.PrincipalFromContext(req.Context())
				res, err := manager.PublishToUser(req.Context(), sse.UserKeyOf(caller.Issuer, caller.TenantID, in.User), sse.Event{Name: "notification", Data: map[string]string{"message": in.Message}})
				switch {
				case errors.Is(err, sse.ErrTransportUnavailable):
					_ = nhttp.SendJSON(w, req, http.StatusServiceUnavailable, "notifications are temporarily unavailable")
				case err != nil:
					_ = nhttp.SendJSON(w, req, http.StatusBadRequest, "invalid notification")
				default:
					// Accepted is broker acceptance or local dispatch, not browser receipt.
					_ = nhttp.SendJSON(w, req, http.StatusAccepted, map[string]bool{"accepted": res.Accepted})
				}
			})

			r.Get("/stats", func(w http.ResponseWriter, req *http.Request) {
				_ = nhttp.SendJSON(w, req, http.StatusOK, manager.Stats())
			})

			// Explicit timeout opt-in for one ordinary route (handler_timeout is
			// off by default). Streams are exempt from the global one; a
			// route-level Timeout on a stream would bound it on purpose.
			r.With(nhttp.Timeout(2*time.Second)).Get("/slow", func(w http.ResponseWriter, req *http.Request) {
				select {
				case <-req.Context().Done():
					_ = nhttp.SendJSON(w, req, http.StatusGatewayTimeout, "timed out")
				case <-time.After(5 * time.Second):
					_ = nhttp.SendJSON(w, req, http.StatusOK, "done")
				}
			})
			return module.Hooks{}, nil
		},
	})
}

// cookieToBearer lifts the session cookie into the Authorization header when
// the request has none. Only this route group does it; nothing else reads the cookie.
func cookieToBearer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			if c, err := r.Cookie(cookieName); err == nil && c.Value != "" {
				r.Header.Set("Authorization", "Bearer "+c.Value)
			}
		}
		next.ServeHTTP(w, r)
	})
}
