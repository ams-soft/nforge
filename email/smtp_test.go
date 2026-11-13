package email

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

func testEnv() envelope {
	e, err := build(testFrom, Message{To: "rcpt@example.org", Subject: "Hello", Text: "body\n.leading dot\n", HTML: "<p>x</p>"}, time.Now())
	if err != nil {
		panic(err)
	}
	return e
}

func ctx5(t *testing.T) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return c
}

func TestStartTLSAccepted(t *testing.T) {
	s := startServer(t, nil)
	if err := s.driver(SecurityStartTLS, "", "").send(ctx5(t), testEnv()); err != nil {
		t.Fatal(err)
	}
	got := s.got()
	if len(got) != 1 || !strings.Contains(got[0], "Subject: Hello") || !strings.Contains(got[0], ".leading dot") {
		t.Fatalf("server got %q", got)
	}
}

func TestImplicitTLSAccepted(t *testing.T) {
	s := startServer(t, func(s *fakeServer) { s.implicit = true })
	if err := s.driver(SecurityTLS, "", "").send(ctx5(t), testEnv()); err != nil {
		t.Fatal(err)
	}
	if len(s.got()) != 1 {
		t.Fatal("not received")
	}
}

func TestAuthOnlyOverTLS(t *testing.T) {
	s := startServer(t, func(s *fakeServer) { s.auth = true })
	if err := s.driver(SecurityStartTLS, "user", "pw").send(ctx5(t), testEnv()); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.authLine == "" || s.authInClear {
		t.Fatalf("auth %q inClear=%v", s.authLine, s.authInClear)
	}
}

func TestNoAuthWhenNotConfigured(t *testing.T) {
	s := startServer(t, func(s *fakeServer) { s.auth = true })
	if err := s.driver(SecurityStartTLS, "", "").send(ctx5(t), testEnv()); err != nil {
		t.Fatal(err)
	}
	if s.authLine != "" {
		t.Fatal("AUTH sent without credentials")
	}
}

func TestStartTLSNotOfferedRefusesAndNeverSendsCredentials(t *testing.T) {
	s := startServer(t, func(s *fakeServer) { s.noStart = true; s.auth = true })
	err := s.driver(SecurityStartTLS, "user", "pw").send(ctx5(t), testEnv())
	var ee *Error
	if !errors.As(err, &ee) || ee.Phase != "starttls" || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if s.authLine != "" || len(s.got()) != 0 {
		t.Fatal("continued without TLS")
	}
}

func TestCertificateMismatchRefused(t *testing.T) {
	for _, security := range []string{SecurityStartTLS, SecurityTLS} {
		s := startServer(t, func(s *fakeServer) { s.implicit = security == SecurityTLS; s.auth = true })
		d := s.driver(security, "user", "pw")
		d.cfg.Host = "other.test" // the certificate is for mail.test
		err := d.send(ctx5(t), testEnv())
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("%s: err = %v", security, err)
		}
		if s.authLine != "" || len(s.got()) != 0 {
			t.Fatalf("%s: data or credentials sent over an unverified connection", security)
		}
	}
}

func TestUntrustedCertificateRefused(t *testing.T) {
	s := startServer(t, nil)
	d := s.driver(SecurityStartTLS, "", "")
	d.tlsConfig = nil // system roots: the test certificate is not trusted
	if err := d.send(ctx5(t), testEnv()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

func TestRcptRejections(t *testing.T) {
	for reply, want := range map[string]error{"550 no such user": ErrRejected, "451 try later": ErrTransient} {
		s := startServer(t, func(s *fakeServer) { s.rcptReply = reply })
		err := s.driver(SecurityStartTLS, "", "").send(ctx5(t), testEnv())
		var ee *Error
		if !errors.Is(err, want) || !errors.As(err, &ee) || ee.Phase != "rcpt" || ee.Code == 0 {
			t.Fatalf("%q: err = %v", reply, err)
		}
		if strings.Contains(err.Error(), "no such user") || strings.Contains(err.Error(), "rcpt@example.org") {
			t.Fatalf("error leaks server text or recipient: %v", err)
		}
	}
}

func TestDataRejectionIsDefinite(t *testing.T) {
	s := startServer(t, func(s *fakeServer) { s.dataReply = "554 spam detected" })
	err := s.driver(SecurityStartTLS, "", "").send(ctx5(t), testEnv())
	if !errors.Is(err, ErrRejected) || errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("err = %v", err)
	}
}

func TestAmbiguousCompletion(t *testing.T) {
	s := startServer(t, func(s *fakeServer) { s.dropAfterData = true })
	err := s.driver(SecurityStartTLS, "", "").send(ctx5(t), testEnv())
	if !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("dropped connection: err = %v", err)
	}
	if s.conns != 1 {
		t.Fatalf("%d connections: hidden retry", s.conns)
	}

	s2 := startServer(t, func(s *fakeServer) { s.hangAfterData = true })
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = s2.driver(SecurityStartTLS, "", "").send(ctx, testEnv())
	if !errors.Is(err, ErrOutcomeUnknown) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline during completion: err = %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("deadline was not enforced mid-protocol")
	}
}

func TestCancelMidDial(t *testing.T) {
	d := &smtpDriver{cfg: SMTPConfig{Host: "mail.test", Port: 25, Security: SecurityStartTLS},
		dial: func(ctx context.Context, _ string) (net.Conn, error) { <-ctx.Done(); return nil, ctx.Err() }}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	err := d.send(ctx, testEnv())
	if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

func TestCancelMidProtocolStalledGreeting(t *testing.T) {
	// A server that accepts and never speaks: the greeting read must end with ctx.
	s := startServer(t, nil)
	_, port := s.addr()
	d := s.driver(SecurityStartTLS, "", "")
	_ = port
	d.dial = func(ctx context.Context, _ string) (net.Conn, error) {
		a, b := net.Pipe()
		t.Cleanup(func() { b.Close() })
		return a, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := d.send(ctx, testEnv())
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 3*time.Second {
		t.Fatalf("err = %v after %v", err, time.Since(start))
	}
}

func TestConcurrentSends(t *testing.T) {
	s := startServer(t, nil)
	d := s.driver(SecurityStartTLS, "", "")
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for range 12 {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- d.send(ctx5(t), testEnv()) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(s.got()) != 12 {
		t.Fatalf("got %d messages", len(s.got()))
	}
}
