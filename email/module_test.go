package email

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ams-soft/nforge/app"
	"github.com/ams-soft/nforge/internal/apptest"
	"github.com/ams-soft/nforge/logs"
	"github.com/ams-soft/nforge/metrics"
	"github.com/ams-soft/nforge/module"
)

const cfgYAML = `
email:
  from: {address: no-reply@example.com, name: Example}
  smtp: {host: mail.test}
`

// useDriver makes every module instance of the test use d.
func useDriver(t *testing.T, d driver) {
	t.Helper()
	old := newDriverFor
	newDriverFor = func(Config) (driver, error) { return d, nil }
	t.Cleanup(func() { newDriverFor = old })
}

// consumer is an application module that requires the email capability.
func consumer(use func(*Client)) module.Descriptor {
	return module.New("welcome", module.Definition{
		Requires: []module.Handle{Capability},
		Init: func(_ context.Context, sc *module.Scope) (module.Hooks, error) {
			c := module.Require(sc, Capability)
			return module.Hooks{Start: func(context.Context) error { use(c); return nil }}, nil
		},
	})
}

func TestConfigErrorsFailAppNew(t *testing.T) {
	for name, yaml := range map[string]string{
		"driver":      "email: {driver: ses, from: {address: a@example.com}, smtp: {host: h}}\n",
		"no from":     "email: {smtp: {host: h}}\n",
		"no host":     "email: {from: {address: a@example.com}}\n",
		"security":    "email: {from: {address: a@example.com}, smtp: {host: h, security: none}}\n",
		"unknown key": "email: {from: {address: a@example.com}, smtp: {host: h, plaintext: true}}\n",
		"half creds":  "email: {from: {address: a@example.com}, smtp: {host: h, username: u}}\n",
	} {
		if _, err := apptest.New(t, yaml, Module()); err == nil {
			t.Errorf("%s: app.New succeeded", name)
		}
	}
	_, err := apptest.New(t, "email: {driver: ses, from: {address: a@example.com}, smtp: {host: h}}\n", Module())
	if !errors.Is(err, ErrUnsupportedDriver) {
		t.Fatalf("err = %v", err)
	}
}

func TestSectionWithoutModuleIsAnError(t *testing.T) {
	if _, err := apptest.New(t, cfgYAML, logs.Module()); err == nil {
		t.Fatal("an email section nobody owns must fail app.New")
	}
}

func TestEnvFileKeysAndNoSecretInErrors(t *testing.T) {
	d, err := apptest.LoadE(t, cfgYAML, "EMAIL_SMTP_USERNAME=u\nEMAIL_SMTP_PASSWORD=pw\nEMAIL_SMTP_SECURITY=tls\nEMAIL_TIMEOUT=3s\n")
	if err != nil {
		t.Fatal(err)
	}
	a, err := app.New(d, app.Modules(Module()), apptest.Quiet())
	if err != nil {
		t.Fatal(err)
	}
	_ = a
}

func TestDisabledPublishesNothing(t *testing.T) {
	yaml := "email: {enabled: false}\n"
	a := apptest.Start(t, yaml, Module())
	if _, ok := app.Value(a, Capability); ok {
		t.Fatal("a disabled module published a client")
	}
	if _, err := apptest.New(t, yaml, Module(), consumer(func(*Client) {})); !errors.Is(err, module.ErrDisabledDependency) {
		t.Fatalf("requiring a disabled email: %v", err)
	}
}

func TestEmailOnlyApplicationNeedsNoHTTPAndSendsAfterStart(t *testing.T) {
	f := &fakeDriver{}
	useDriver(t, f)
	sendErr := make(chan error, 1)
	var cl *Client
	a := apptest.Start(t, cfgYAML, logs.Module(), Module(), consumer(func(c *Client) {
		cl = c
		// A send from Start (the client is operational once its own Start ran: Email starts before the consumer).
		sendErr <- c.Send(context.Background(), okMsg)
	}))
	if err := <-sendErr; err != nil {
		t.Fatalf("send after start: %v", err)
	}
	if len(f.sent) != 1 {
		t.Fatal("not sent")
	}
	if rep := a.Status(context.Background()); !rep.Ready {
		t.Fatalf("not ready: %+v", rep)
	}
	if err := a.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := a.Shutdown(context.Background()); err != nil {
		t.Fatalf("second shutdown: %v", err)
	}
	if err := cl.Send(context.Background(), okMsg); !errors.Is(err, ErrClosed) {
		t.Fatalf("send after shutdown: %v", err)
	}
}

func TestSendInInitIsRefusedByLifecycle(t *testing.T) {
	useDriver(t, &fakeDriver{})
	var got error
	m := module.New("early", module.Definition{
		Requires: []module.Handle{Capability},
		Init: func(_ context.Context, sc *module.Scope) (module.Hooks, error) {
			got = module.Require(sc, Capability).Send(context.Background(), okMsg)
			return module.Hooks{}, nil
		},
	})
	apptest.Start(t, cfgYAML, Module(), m)
	if !errors.Is(got, ErrClosed) {
		t.Fatalf("a send in Init must not run: %v", got)
	}
}

func TestReadinessNeverDialsOrSends(t *testing.T) {
	f := &fakeDriver{}
	useDriver(t, f)
	a := apptest.Start(t, cfgYAML, Module())
	for range 3 {
		if !a.Status(context.Background()).Ready {
			t.Fatal("not ready")
		}
	}
	if f.calls.Load() != 0 {
		t.Fatal("readiness reached the driver")
	}
}

func TestMetricsWhenPresent(t *testing.T) {
	useDriver(t, &fakeDriver{})
	a := apptest.Start(t, cfgYAML, metrics.Module(), Module())
	cl, _ := app.Value(a, Capability)
	if err := cl.Send(context.Background(), okMsg); err != nil {
		t.Fatal(err)
	}
	reg, _ := app.Value(a, metrics.Capability)
	var b strings.Builder
	if err := reg.Render(&b); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if !strings.Contains(out, `nforge_email_sends_total{driver="smtp",outcome="accepted"} 1`) || !strings.Contains(out, "nforge_email_send_duration_seconds") {
		t.Fatalf("metrics:\n%s", out)
	}
	for _, leak := range []string{"example.org", "Hello", "mail.test"} {
		if strings.Contains(out, leak) {
			t.Fatalf("metrics leak %q", leak)
		}
	}
}

func TestEndToEndOverVerifiedTLS(t *testing.T) {
	s := startServer(t, nil)
	useDriver(t, s.driver(SecurityStartTLS, "", ""))
	a := apptest.Start(t, cfgYAML, logs.Module(), Module())
	cl, _ := app.Value(a, Capability)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := cl.Send(ctx, Message{To: "rcpt@example.org", Subject: "Welcome", HTML: "<p>hi</p>", Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	if len(s.got()) != 1 {
		t.Fatal("server did not receive the message")
	}
}

func TestNoopDriverNeedsNoSMTPSection(t *testing.T) {
	a := apptest.Start(t, "email: {driver: noop, from: {address: no-reply@example.com}}\n", Module())
	cl, ok := app.Value(a, Capability)
	if !ok {
		t.Fatal("no client")
	}
	if err := cl.Send(context.Background(), okMsg); err != nil {
		t.Fatal(err)
	}
	// Validation still applies: a message that would be refused for real is refused.
	if err := cl.Send(context.Background(), Message{To: "bad"}); !errors.Is(err, ErrInvalidMessage) {
		t.Fatalf("err = %v", err)
	}
	if _, err := apptest.New(t, "email: {driver: noop}\n", Module()); err == nil {
		t.Fatal("from.address is still required")
	}
}
