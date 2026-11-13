// Command email shows the email module in an application with no HTTP server,
// database or broker: a welcome module renders welcome.mjml with
// email.RenderMJML and sends it once, after the application started, through
// the SMTP client resolved by dependency injection.
//
//	set -a; . _examples/email/.env; set +a
//	go run ./_examples/email -config _examples/email/config.yaml -to you@example.com
//
// A successful run means the SMTP server ACCEPTED the message. That is not a
// delivery: bounces and spam filtering happen later and are not observable.
package main

import (
	"context"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/ams-soft/nforge/app"
	"github.com/ams-soft/nforge/config"
	"github.com/ams-soft/nforge/email"
	"github.com/ams-soft/nforge/logs"
	"github.com/ams-soft/nforge/module"
)

//go:embed welcome.mjml
var welcomeMJML string

func main() {
	to := flag.String("to", os.Getenv("EMAIL_TO"), "recipient of the welcome message (default: $EMAIL_TO)")
	cfg, err := config.Load() // parses the flags, -to included
	if err != nil {
		fmt.Fprintln(os.Stderr, "[Fatal ERROR] unable to load config:", err)
		os.Exit(1)
	}
	if os.Getenv("SMTP_HOST") == "" {
		fmt.Fprintln(os.Stderr, "SMTP_HOST is not set: copy _examples/email/.env.example to .env, fill it in and export it (nothing was sent).")
		os.Exit(2)
	}
	os.Exit(run(context.Background(), cfg, *to, os.Stdout, os.Stderr))
}

// run starts the application, sends once and shuts it down. It returns the exit
// code: 0 accepted, 1 failure, 2 missing input.
func run(ctx context.Context, cfg *config.Document, to string, stdout, stderr io.Writer) int {
	if to == "" {
		fmt.Fprintln(stderr, "no recipient: pass -to you@example.com or set EMAIL_TO (nothing was sent).")
		return 2
	}
	a, w, err := newApp(cfg)
	if err != nil {
		fmt.Fprintln(stderr, "[Fatal ERROR]", err)
		return 1
	}
	if err := a.Start(ctx); err != nil { // sending is only allowed once the application started
		fmt.Fprintln(stderr, "[Fatal ERROR]", err)
		return 1
	}
	code := 0
	if err := w.send(ctx, to); err != nil {
		fmt.Fprintln(stderr, "not sent:", describe(err))
		code = 1
	} else {
		fmt.Fprintln(stdout, "The SMTP server accepted the message. That is not a delivery confirmation.")
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	if err := a.Shutdown(sctx); err != nil {
		fmt.Fprintln(stderr, "shutdown:", err)
		code = 1
	}
	return code
}

// describe turns an email error into a user-facing sentence.
func describe(err error) string {
	switch {
	case errors.Is(err, email.ErrInvalidMessage):
		return err.Error()
	case errors.Is(err, email.ErrOutcomeUnknown):
		return "the connection failed after the message was sent; it may or may not have been accepted, do not resend blindly (" + err.Error() + ")"
	case errors.Is(err, email.ErrRejected):
		return "the server permanently refused the message (" + err.Error() + ")"
	case errors.Is(err, email.ErrTransient):
		return "the server refused the message for now; try again later (" + err.Error() + ")"
	case errors.Is(err, email.ErrUnavailable):
		return "the server could not be reached or trusted: check host, port, security and credentials (" + err.Error() + ")"
	}
	return err.Error()
}

// welcomer is the application module's state: the client it resolved in Init.
type welcomer struct{ client *email.Client }

// newApp registers the modules; the tests reuse it. The welcome module
// requires email.Capability and sends nothing in Init.
func newApp(cfg *config.Document, opts ...app.Option) (*app.Application, *welcomer, error) {
	w := &welcomer{}
	welcome := module.New("welcome", module.Definition{
		Requires: []module.Handle{email.Capability},
		Init: func(_ context.Context, sc *module.Scope) (module.Hooks, error) {
			w.client = module.Require(sc, email.Capability)
			return module.Hooks{}, nil
		},
	})
	mods := app.Modules(logs.Module(), email.Module(), welcome)
	a, err := app.New(cfg, append([]app.Option{mods}, opts...)...)
	return a, w, err
}

// send renders the MJML fixture and sends it. The MJML is a constant here; a
// value interpolated into MJML from untrusted input would have to be escaped first.
func (w *welcomer) send(ctx context.Context, to string) error {
	html, err := email.RenderMJML(ctx, welcomeMJML)
	if err != nil {
		return fmt.Errorf("render welcome.mjml: %w", err)
	}
	return w.client.Send(ctx, email.Message{
		To:      to,
		Subject: "Welcome to nForge",
		Text:    "Welcome to nForge. This message was sent by the nForge email module.",
		HTML:    html,
	})
}
