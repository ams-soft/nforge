package email

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ams-soft/nforge/metrics"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Client sends email through the configured driver. It is safe for concurrent
// use. Send returns nil only when the server accepted the message: that is not
// delivery, bounces and spam filtering happen later and are not observable here.
type Client struct {
	cfg    Config
	drv    driver
	logger *slog.Logger
	now    func() time.Time
	tracer trace.Tracer // nil without the tracing module

	sends    *metrics.CounterVec   // nil without the metrics module
	duration *metrics.HistogramVec // nil without the metrics module

	mu       sync.Mutex
	started  bool
	closed   bool
	inflight sync.WaitGroup
	base     context.Context // canceled when a shutdown gives up waiting
	cancel   context.CancelFunc
	stopOnce sync.Once
	stopped  chan struct{}
	stopErr  error
}

func newClient(cfg Config, drv driver, logger *slog.Logger) *Client {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	base, cancel := context.WithCancel(context.Background())
	return &Client{cfg: cfg, drv: drv, logger: logger, now: time.Now, base: base, cancel: cancel, stopped: make(chan struct{})}
}

// Send submits one message synchronously and returns after the server accepted
// it or the attempt failed; it never retries. The whole operation is bounded by
// email.timeout and by ctx, whichever ends first.
//
// Errors wrap ErrInvalidMessage (nothing was attempted), ErrClosed,
// ErrUnavailable, ErrTransient, ErrRejected or ErrOutcomeUnknown, and the
// context error when the context ended it. They never contain the message,
// the recipient, a credential or the server's text.
func (c *Client) Send(ctx context.Context, msg Message) (err error) {
	env, err := build(c.cfg.From, msg, c.now())
	if err != nil {
		c.observe(ctx, "invalid", 0, err)
		return err
	}
	c.mu.Lock()
	if c.closed || !c.started {
		c.mu.Unlock()
		return ErrClosed
	}
	c.inflight.Add(1)
	c.mu.Unlock()
	defer c.inflight.Done()

	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	stop := context.AfterFunc(c.base, cancel) // a shutdown past its deadline ends the send
	defer stop()

	if c.tracer != nil {
		var span trace.Span
		ctx, span = c.tracer.Start(ctx, "email.send", trace.WithSpanKind(trace.SpanKindClient),
			trace.WithAttributes(attribute.String("email.driver", c.cfg.Driver)))
		defer func() {
			if err != nil {
				span.SetStatus(codes.Error, "email send failed") // no recipient, no server text
			}
			span.End()
		}()
	}
	start := c.now()
	err = c.drv.send(ctx, env)
	if err != nil && c.base.Err() != nil {
		err = errors.Join(ErrClosed, err)
	}
	c.observe(ctx, outcome(err), c.now().Sub(start), err)
	return err
}

func outcome(err error) string {
	switch {
	case err == nil:
		return "accepted"
	case errors.Is(err, ErrOutcomeUnknown):
		return "unknown"
	case errors.Is(err, ErrRejected):
		return "rejected"
	case errors.Is(err, ErrTransient):
		return "transient"
	case errors.Is(err, ErrClosed):
		return "closed"
	default:
		return "failed"
	}
}

// observe records the metric and one log line; it never carries the recipient,
// subject, body or server text.
func (c *Client) observe(ctx context.Context, out string, d time.Duration, err error) {
	if c.sends != nil {
		c.sends.Inc(c.cfg.Driver, out)
	}
	if c.duration != nil && d > 0 {
		c.duration.Observe(d.Seconds(), c.cfg.Driver)
	}
	if err == nil {
		c.logger.DebugContext(ctx, "[EmailModule] Message accepted by the SMTP server", "driver", c.cfg.Driver)
		return
	}
	var ee *Error
	attrs := []any{"driver", c.cfg.Driver, "outcome", out}
	if errors.As(err, &ee) {
		attrs = append(attrs, "phase", ee.Phase, "code", ee.Code)
	}
	c.logger.WarnContext(ctx, "[EmailModule] Send failed", attrs...)
}

func (c *Client) registerMetrics(reg *metrics.Registry) {
	c.sends = reg.Counter("nforge_email_sends_total", "Email sends by driver and outcome (accepted by the server, not delivered).", "driver", "outcome")
	c.duration = reg.Histogram("nforge_email_send_duration_seconds", "Email send time by driver.", nil, "driver")
}

func (c *Client) start() {
	c.mu.Lock()
	c.started = true
	c.mu.Unlock()
}

// ready reflects local state only: it never dials the server and never sends.
func (c *Client) ready(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return ErrClosed
	}
	return nil
}

// stop refuses new sends and waits for the ones in flight until ctx ends; then
// it cancels them (their connections are closed) and waits for them to return.
// It is idempotent: every call waits for the first and returns its result.
func (c *Client) stop(ctx context.Context) error {
	c.stopOnce.Do(func() {
		defer close(c.stopped)
		c.mu.Lock()
		c.closed = true
		c.mu.Unlock()
		done := make(chan struct{})
		go func() { c.inflight.Wait(); close(done) }()
		select {
		case <-done:
		case <-ctx.Done():
			c.cancel()
			<-done
			c.stopErr = fmt.Errorf("email: in-flight sends canceled at shutdown: %w", ctx.Err())
		}
		c.cancel()
	})
	<-c.stopped
	return c.stopErr
}
