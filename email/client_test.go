package email

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeDriver substitutes the transport.
type fakeDriver struct {
	mu      sync.Mutex
	sent    []envelope
	err     error
	block   chan struct{} // when set, send waits on it or on ctx
	started chan struct{}
	calls   atomic.Int32
}

func (f *fakeDriver) send(ctx context.Context, e envelope) error {
	f.calls.Add(1)
	if f.started != nil {
		f.started <- struct{}{}
	}
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return fmt.Errorf("fake: %w", ctx.Err())
		}
	}
	if f.err != nil {
		return f.err
	}
	f.mu.Lock()
	f.sent = append(f.sent, e)
	f.mu.Unlock()
	return nil
}

func testCfg() Config {
	c := Config{From: testFrom, SMTP: SMTPConfig{Host: "mail.test"}}
	c.ApplyDefaults()
	return c
}

func started(t *testing.T, d driver, logger *slog.Logger) *Client {
	t.Helper()
	c := newClient(testCfg(), d, logger)
	c.start()
	return c
}

var okMsg = Message{To: "rcpt@example.org", Subject: "Hello", Text: "hi"}

func TestSendThroughFakeDriver(t *testing.T) {
	f := &fakeDriver{}
	c := started(t, f, nil)
	if err := c.Send(context.Background(), okMsg); err != nil {
		t.Fatal(err)
	}
	if len(f.sent) != 1 || f.sent[0].to != "rcpt@example.org" || f.sent[0].from != "no-reply@example.com" {
		t.Fatalf("sent %+v", f.sent)
	}
}

func TestInvalidMessageNeverReachesTheDriver(t *testing.T) {
	f := &fakeDriver{}
	c := started(t, f, nil)
	if err := c.Send(context.Background(), Message{To: "x"}); !errors.Is(err, ErrInvalidMessage) {
		t.Fatalf("err = %v", err)
	}
	if f.calls.Load() != 0 {
		t.Fatal("driver called")
	}
}

func TestSendBeforeStartAndAfterStop(t *testing.T) {
	f := &fakeDriver{}
	c := newClient(testCfg(), f, nil)
	if err := c.Send(context.Background(), okMsg); !errors.Is(err, ErrClosed) {
		t.Fatalf("before start: %v", err)
	}
	c.start()
	if err := c.stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := c.Send(context.Background(), okMsg); !errors.Is(err, ErrClosed) {
		t.Fatalf("after stop: %v", err)
	}
	if c.ready(context.Background()) == nil {
		t.Fatal("a stopped client must not be ready")
	}
}

func TestNoRetry(t *testing.T) {
	f := &fakeDriver{err: &Error{Phase: "data", kind: ErrOutcomeUnknown}}
	c := started(t, f, nil)
	if err := c.Send(context.Background(), okMsg); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatal(err)
	}
	if f.calls.Load() != 1 {
		t.Fatalf("%d driver calls", f.calls.Load())
	}
}

func TestTimeoutBoundsTheSend(t *testing.T) {
	f := &fakeDriver{block: make(chan struct{})}
	cfg := testCfg()
	cfg.Timeout = 50 * time.Millisecond
	c := newClient(cfg, f, nil)
	c.start()
	start := time.Now()
	err := c.Send(context.Background(), okMsg)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 2*time.Second {
		t.Fatalf("err = %v after %v", err, time.Since(start))
	}
}

func TestShorterCallerDeadlineWins(t *testing.T) {
	f := &fakeDriver{block: make(chan struct{})}
	c := started(t, f, nil) // 15s timeout
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := c.Send(ctx, okMsg); !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 2*time.Second {
		t.Fatalf("err = %v", err)
	}
}

func TestStopWaitsForInFlight(t *testing.T) {
	f := &fakeDriver{block: make(chan struct{}), started: make(chan struct{}, 1)}
	c := started(t, f, nil)
	res := make(chan error, 1)
	go func() { res <- c.Send(context.Background(), okMsg) }()
	<-f.started
	stopped := make(chan error, 1)
	go func() { stopped <- c.stop(context.Background()) }()
	select {
	case err := <-stopped:
		t.Fatalf("stop returned while a send was in flight: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := c.Send(context.Background(), okMsg); !errors.Is(err, ErrClosed) {
		t.Fatalf("send during shutdown: %v", err)
	}
	close(f.block)
	if err := <-res; err != nil {
		t.Fatalf("in-flight send: %v", err)
	}
	if err := <-stopped; err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestStopDeadlineCancelsInFlight(t *testing.T) {
	f := &fakeDriver{block: make(chan struct{}), started: make(chan struct{}, 1)}
	c := started(t, f, nil)
	res := make(chan error, 1)
	go func() { res <- c.Send(context.Background(), okMsg) }()
	<-f.started
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := c.stop(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stop: %v", err)
	}
	if err := <-res; !errors.Is(err, ErrClosed) {
		t.Fatalf("canceled send: %v", err)
	}
	if err2 := c.stop(context.Background()); err2 == nil || err2.Error() != err.Error() {
		t.Fatalf("second stop = %v, want the first result", err2)
	}
}

func TestConcurrentSendsOnClient(t *testing.T) {
	f := &fakeDriver{}
	c := started(t, f, nil)
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := c.Send(context.Background(), okMsg); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if len(f.sent) != 50 {
		t.Fatalf("sent %d", len(f.sent))
	}
}

func TestLogsCarryNoPersonalData(t *testing.T) {
	var buf syncBuf
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	f := &fakeDriver{}
	c := started(t, f, logger)
	_ = c.Send(context.Background(), Message{To: "secret.person@example.org", Subject: "Top secret subject", Text: "secret body"})
	f.err = &Error{Phase: "rcpt", Code: 550, kind: ErrRejected}
	_ = c.Send(context.Background(), Message{To: "secret.person@example.org", Subject: "Top secret subject", Text: "secret body"})
	out := buf.String()
	if !strings.Contains(out, "[EmailModule]") {
		t.Fatalf("no log: %q", out)
	}
	for _, leak := range []string{"secret", "example.org", "Top secret"} {
		if strings.Contains(out, leak) {
			t.Fatalf("log leaks %q: %s", leak, out)
		}
	}
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }
