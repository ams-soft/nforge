package email

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/smtp"
	"net/textproto"
	"os"
	"strconv"
	"time"
)

// driver is the transport boundary: it sends one validated envelope and returns
// only after the server accepted it or the attempt failed. Drivers keep no state
// that outlives a call here, so the client needs no Close for SMTP.
type driver interface {
	send(ctx context.Context, e envelope) error
}

// newDriver is the explicit factory of the configured driver.
func newDriver(cfg Config) (driver, error) {
	switch cfg.Driver {
	case DriverSMTP:
		return &smtpDriver{cfg: cfg.SMTP}, nil
	case DriverNoop:
		return noopDriver{}, nil
	default:
		return nil, ErrUnsupportedDriver
	}
}

// noopDriver accepts every envelope and sends nothing: for tests and local
// runs. Validation and MIME encoding still happen before it, so a message that
// would be refused for real is refused here too.
type noopDriver struct{}

func (noopDriver) send(context.Context, envelope) error { return nil }

// smtpDriver opens one connection per send: simple, safe for concurrent callers
// and free of reuse, idle and shutdown edge cases.
type smtpDriver struct {
	cfg SMTPConfig
	// tlsConfig is the base TLS configuration; tests inject a root pool through
	// it. ServerName and MinVersion are always set by the driver.
	tlsConfig *tls.Config
	// dial replaces the network dial in tests; nil dials TCP.
	dial func(ctx context.Context, addr string) (net.Conn, error)
}

func (d *smtpDriver) tlsCfg() *tls.Config {
	var c *tls.Config
	if d.tlsConfig != nil {
		c = d.tlsConfig.Clone()
	} else {
		c = &tls.Config{}
	}
	c.ServerName = d.cfg.Host // peer-name verification; never skipped
	c.MinVersion = tls.VersionTLS12
	c.InsecureSkipVerify = false
	return c
}

func (d *smtpDriver) send(ctx context.Context, e envelope) (err error) {
	addr := net.JoinHostPort(d.cfg.Host, strconv.Itoa(d.cfg.Port))
	phase := "connect"
	fail := func(err error) error { return classify(ctx, phase, err) }

	conn, err := d.connect(ctx, addr)
	if err != nil {
		return fail(err)
	}
	// One watcher turns cancellation or the deadline into an immediate I/O
	// failure on the connection, so no protocol step can outlive ctx.
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	stop := make(chan struct{})
	watched := make(chan struct{})
	go func() {
		defer close(watched)
		select {
		case <-ctx.Done():
			_ = conn.SetDeadline(time.Unix(1, 0))
		case <-stop:
		}
	}()
	defer func() { close(stop); <-watched; _ = conn.Close() }()

	if d.cfg.Security == SecurityTLS {
		phase = "tls"
		if err := conn.(*tls.Conn).HandshakeContext(ctx); err != nil {
			return fail(err)
		}
	}
	phase = "connect"
	c, err := smtp.NewClient(conn, d.cfg.Host) // reads the 220 greeting
	if err != nil {
		return fail(err)
	}
	if d.cfg.Security == SecurityStartTLS {
		phase = "starttls"
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return &Error{Phase: phase, kind: ErrUnavailable} // never continue in clear text
		}
		if err := c.StartTLS(d.tlsCfg()); err != nil {
			return fail(err)
		}
	}
	if d.cfg.Username != "" {
		phase = "auth"
		if ok, _ := c.Extension("AUTH"); !ok {
			return &Error{Phase: phase, kind: ErrUnavailable}
		}
		// PlainAuth refuses to send credentials unless the connection is TLS (or loopback).
		if err := c.Auth(smtp.PlainAuth("", d.cfg.Username, d.cfg.Password, d.cfg.Host)); err != nil {
			return fail(err)
		}
	}
	phase = "mail"
	if err := c.Mail(e.from); err != nil {
		return fail(err)
	}
	phase = "rcpt"
	if err := c.Rcpt(e.to); err != nil {
		return fail(err)
	}
	phase = "data"
	w, err := c.Data()
	if err != nil {
		return fail(err)
	}
	phase = "body"
	if _, err := w.Write(e.data); err != nil {
		return unknown(ctx, phase, err)
	}
	if err := w.Close(); err != nil { // the server's verdict on the whole message
		var pe *textproto.Error
		if errors.As(err, &pe) {
			return fail(err) // a definite answer: not accepted
		}
		return unknown(ctx, phase, err)
	}
	_ = c.Quit() // accepted already; a failing QUIT changes nothing
	return nil
}

func (d *smtpDriver) connect(ctx context.Context, addr string) (net.Conn, error) {
	var conn net.Conn
	var err error
	if d.dial != nil {
		conn, err = d.dial(ctx, addr)
	} else {
		var nd net.Dialer
		conn, err = nd.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return nil, err
	}
	if d.cfg.Security == SecurityTLS {
		return tls.Client(conn, d.tlsCfg()), nil
	}
	return conn, nil
}

// classify maps a failure before the message was sent to the public error. A
// textproto.Error is the server's reply code; anything else is the transport.
func classify(ctx context.Context, phase string, err error) error {
	e := &Error{Phase: phase}
	var pe *textproto.Error
	switch {
	case ctxCause(ctx, err) != nil:
		e.err = ctxCause(ctx, err)
		e.kind = ErrUnavailable
	case errors.As(err, &pe):
		e.Code = pe.Code
		if pe.Code >= 500 {
			e.kind = ErrRejected
		} else {
			e.kind = ErrTransient
		}
	default:
		e.kind = ErrUnavailable
	}
	return e
}

// unknown is a failure after the body left: the outcome is ambiguous.
func unknown(ctx context.Context, phase string, err error) error {
	return &Error{Phase: phase, kind: ErrOutcomeUnknown, err: ctxCause(ctx, err)}
}

// ctxCause reports the context error behind a failure. The connection deadline
// is the context's own, so its timeout can surface a moment before ctx.Err()
// does: that is still the deadline.
func ctxCause(ctx context.Context, err error) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return nil
}
