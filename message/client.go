package message

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ams-soft/nforge/codec"
	"github.com/ams-soft/nforge/module"
	amqp "github.com/rabbitmq/amqp091-go"
)

// Capability is the client published by Module.
var Capability = module.NewCapability[*Client]("message")

type publisherOptions struct {
	priority        uint8
	prioritySet     bool
	id, contentType string
	headers         map[string]string
}
type publishOptions struct{ publisherOptions }

// PublishOption configures one publication.
type PublishOption func(*publishOptions) error

// WithPriority sets native Classic priority.
func WithPriority(n uint8) PublishOption {
	return func(o *publishOptions) error { o.priority = n; o.prioritySet = true; return nil }
}

// WithMessageID sets a caller supplied message ID.
func WithMessageID(id string) PublishOption {
	return func(o *publishOptions) error { o.id = id; return nil }
}

// WithHeaders adds user headers.
func WithHeaders(h map[string]string) PublishOption {
	return func(o *publishOptions) error { o.headers = mapsClone(h); return nil }
}

// WithContentType overrides the content type for raw byte payloads.
func WithContentType(s string) PublishOption {
	return func(o *publishOptions) error { o.contentType = s; return nil }
}
func mapsClone(m map[string]string) map[string]string {
	n := make(map[string]string, len(m))
	for k, v := range m {
		n[k] = v
	}
	return n
}

// PublishResult identifies the confirmed publication.
type PublishResult struct {
	MessageID string // broker-visible message ID
	Queue     string // destination queue
}

// Client publishes confirmed messages and owns a RabbitMQ connection.
type Client struct {
	cfg              Config
	registry         *codec.Registry
	conn             *amqp.Connection
	pub              *amqp.Channel
	confirm          <-chan amqp.Confirmation
	returns          <-chan amqp.Return
	queues           map[string]QueueSpec
	logger           *slog.Logger
	mu               sync.Mutex
	consumersMu      sync.Mutex
	consumers        map[string]*consumerRegistration
	reprocessMu      sync.Mutex
	reprocessing     map[string]bool
	started          atomic.Bool
	closed           atomic.Bool
	generation       atomic.Uint64
	reconnecting     atomic.Bool
	blocked          atomic.Bool
	lastErr          atomic.Value
	lifeCtx          context.Context
	consumerLogger   *slog.Logger
	failure          func(error)
	closedNotify     <-chan *amqp.Error
	blockedNotify    <-chan amqp.Blocking
	shutdownOnce     sync.Once
	shutdownDone     chan struct{}
	shutdownErr      error
	fanoutDeclared   map[string]bool // exchanges declared on fanoutGeneration (guarded by mu)
	fanoutGeneration uint64
}

func openClient(ctx context.Context, cfg Config, reg *codec.Registry, logger *slog.Logger, queues []QueueSpec) (*Client, error) {
	c := &Client{cfg: cfg, registry: reg, queues: map[string]QueueSpec{}, consumers: map[string]*consumerRegistration{}, reprocessing: map[string]bool{}, shutdownDone: make(chan struct{}), logger: logger}
	for _, q := range queues {
		c.queues[q.name] = q
	}
	if err := c.connect(ctx); err != nil {
		return nil, err
	}
	return c, nil
}
func (c *Client) connect(ctx context.Context) error {
	d := amqp.Config{Heartbeat: c.cfg.RabbitMQ.Heartbeat, Properties: amqp.Table{"connection_name": c.cfg.RabbitMQ.ConnectionName}}
	if strings.HasPrefix(c.cfg.RabbitMQ.URL, "amqps://") {
		t, e := c.cfg.tlsConfig()
		if e != nil {
			return e
		}
		d.TLSClientConfig = t
	}
	dialCtx, cancel := context.WithTimeout(ctx, c.cfg.RabbitMQ.DialTimeout)
	defer cancel()
	type result struct {
		conn *amqp.Connection
		err  error
	}
	ch := make(chan result)
	go func() {
		co, e := amqp.DialConfig(c.cfg.RabbitMQ.URL, d)
		select {
		case ch <- result{co, e}:
		case <-dialCtx.Done():
			if co != nil {
				_ = co.Close()
			}
		}
	}()
	var x result
	select {
	case x = <-ch:
	case <-dialCtx.Done():
		return ErrTransportUnavailable
	}
	if x.err != nil {
		var brokerErr *amqp.Error
		if errors.As(x.err, &brokerErr) && (brokerErr.Code == 403 || brokerErr.Code == 406) {
			return &Error{Op: "connect", Reason: "RabbitMQ rejected the configured connection", Permanent: true}
		}
		return fmt.Errorf("%w: RabbitMQ connection failed", ErrTransportUnavailable)
	}
	pub, e := x.conn.Channel()
	if e != nil {
		_ = x.conn.Close()
		return fmt.Errorf("%w: publisher channel failed", ErrTransportUnavailable)
	}
	if e = pub.Confirm(false); e != nil {
		_ = pub.Close()
		_ = x.conn.Close()
		return fmt.Errorf("%w: publisher confirms unavailable", ErrTransportUnavailable)
	}
	confirm := pub.NotifyPublish(make(chan amqp.Confirmation, 1))
	returns := pub.NotifyReturn(make(chan amqp.Return, 1))
	if e = c.declareTopologyOn(x.conn); e != nil {
		_ = pub.Close()
		_ = x.conn.Close()
		return e
	}
	c.mu.Lock()
	c.conn = x.conn
	c.closedNotify = x.conn.NotifyClose(make(chan *amqp.Error, 1))
	c.blockedNotify = x.conn.NotifyBlocked(make(chan amqp.Blocking, 1))
	c.pub = pub
	c.confirm = confirm
	c.returns = returns
	c.generation.Add(1)
	c.reconnecting.Store(false)
	c.blocked.Store(false)
	c.mu.Unlock()
	return nil
}
func (c *Client) declareTopologyOn(conn *amqp.Connection) error {
	ch, e := conn.Channel()
	if e != nil {
		return fmt.Errorf("%w: topology channel unavailable", ErrTransportUnavailable)
	}
	defer ch.Close()
	for _, q := range c.queues {
		if q.deadLetter {
			if e = ch.ExchangeDeclare(c.cfg.Namespace+".dlx", "direct", true, false, false, false, nil); e != nil {
				return c.topologyError(q, "dead-letter exchange", e)
			}
			if _, e = ch.QueueDeclare(q.name+"_dlq", true, false, false, false, amqp.Table{"x-queue-type": string(q.kind)}); e != nil {
				return c.topologyError(q, "dead-letter queue", e)
			}
			if e = ch.QueueBind(q.name+"_dlq", q.name, c.cfg.Namespace+".dlx", false, nil); e != nil {
				return c.topologyError(q, "dead-letter binding", e)
			}
		}
		args := amqp.Table{}
		args["x-queue-type"] = string(q.kind)
		if q.prioritySet {
			args["x-max-priority"] = int32(q.priority)
		}
		if q.deadLetter {
			args["x-dead-letter-exchange"] = c.cfg.Namespace + ".dlx"
			args["x-dead-letter-routing-key"] = q.name
		}
		if _, e = ch.QueueDeclare(q.name, true, false, false, false, args); e != nil {
			return c.topologyError(q, "queue", e)
		}
		if q.retryDelay > 0 {
			ra := amqp.Table{"x-queue-type": string(q.kind), "x-message-ttl": q.retryDelay.Milliseconds(), "x-dead-letter-exchange": "", "x-dead-letter-routing-key": q.name}
			if _, e = ch.QueueDeclare(q.name+"_retry", true, false, false, false, ra); e != nil {
				return c.topologyError(q, "retry queue", e)
			}
		}
	}
	return nil
}
func (c *Client) topologyError(q QueueSpec, what string, cause error) error {
	var brokerErr *amqp.Error
	permanent := errors.As(cause, &brokerErr) && (brokerErr.Code == 403 || brokerErr.Code == 406)
	reason := what + " declaration failed"
	if permanent && brokerErr.Code == 406 {
		const marker = "inequivalent arg '"
		if i := strings.Index(brokerErr.Reason, marker); i >= 0 {
			attribute := brokerErr.Reason[i+len(marker):]
			if end := strings.IndexByte(attribute, '\''); end > 0 {
				reason += ": conflicting " + attribute[:end]
			}
		}
	}
	return &Error{Op: "declare topology", Queue: q.name, Reason: reason, Permanent: permanent}
}
func (c *Client) usableLocked() error {
	if c.closed.Load() {
		return ErrClosed
	}
	if !c.started.Load() {
		return ErrNotStarted
	}
	if c.reconnecting.Load() || c.conn == nil || c.conn.IsClosed() || c.pub == nil || c.pub.IsClosed() {
		return ErrTransportUnavailable
	}
	return nil
}

func (c *Client) usable() error                { c.mu.Lock(); defer c.mu.Unlock(); return c.usableLocked() }
func (c *Client) connection() *amqp.Connection { c.mu.Lock(); defer c.mu.Unlock(); return c.conn }

var idPattern = regexp.MustCompile(`^[\x21-\x7e]{1,128}$`)
var headerPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func validateHeaders(h map[string]string) error {
	if len(h) > 32 {
		return ErrInvalidHeader
	}
	n := 0
	for k, v := range h {
		if !headerPattern.MatchString(k) || strings.HasPrefix(strings.ToLower(k), "x-") {
			return ErrInvalidHeader
		}
		n += len(k) + len(v)
	}
	if n > 4096 {
		return ErrInvalidHeader
	}
	return nil
}
func newID() (string, error) {
	var b [16]byte
	var timestamp [8]byte
	binary.BigEndian.PutUint64(timestamp[:], uint64(time.Now().UnixMilli()))
	copy(b[:6], timestamp[2:])
	if _, e := rand.Read(b[6:]); e != nil {
		return "", fmt.Errorf("message: generate message id: %w", e)
	}
	b[6] = (b[6] & 0x0f) | 0x70
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
func (c *Client) Publish(ctx context.Context, queue string, payload any, opts ...PublishOption) (*PublishResult, error) {
	if e := c.usable(); e != nil {
		return nil, e
	}
	q, ok := c.queues[queue]
	if !ok {
		return nil, ErrQueueNotRegistered
	}
	o := publishOptions{}
	for _, f := range opts {
		if e := f(&o); e != nil {
			return nil, e
		}
	}
	if e := validateHeaders(o.headers); e != nil {
		return nil, e
	}
	if o.id == "" {
		var e error
		o.id, e = newID()
		if e != nil {
			return nil, e
		}
	} else if !idPattern.MatchString(o.id) {
		return nil, fmt.Errorf("%w: invalid message id", ErrInvalidHeader)
	}
	if o.prioritySet && (!q.prioritySet || o.priority > q.priority) {
		return nil, ErrInvalidPriority
	}
	var body []byte
	ct := o.contentType
	headers := amqp.Table{}
	for k, v := range o.headers {
		headers[k] = v
	}
	if raw, ok := payload.([]byte); ok {
		body = append([]byte(nil), raw...)
		if ct == "" {
			ct = "application/octet-stream"
		}
	} else {
		var e error
		body, e = c.registry.EncodeAs(c.cfg.PayloadFormat, payload)
		if e != nil {
			return nil, fmt.Errorf("message: encode payload: %w", e)
		}
		if ct == "" {
			ct = c.cfg.PayloadFormat
		}
		headers["x-nforge-format"] = c.cfg.PayloadFormat
	}
	if int64(len(body)) > int64(c.cfg.Publisher.MaxPayloadBytes) {
		return nil, ErrPayloadTooLarge
	}
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.usableLocked(); e != nil {
		return nil, e
	}
	priority := uint8(0)
	if q.prioritySet {
		priority = o.priority
	}
	if e := c.pub.PublishWithContext(ctx, "", queue, true, false, amqp.Publishing{Headers: headers, ContentType: ct, DeliveryMode: amqp.Persistent, Priority: priority, MessageId: o.id, Timestamp: time.Now(), Body: body}); e != nil {
		return nil, fmt.Errorf("%w: publish interrupted", ErrPublishUncertain)
	}
	t := time.NewTimer(c.cfg.Publisher.ConfirmTimeout)
	defer t.Stop()
	for {
		select {
		case r := <-c.returns:
			if r.MessageId == o.id {
				return nil, ErrUnroutable
			}
		case ack, ok := <-c.confirm:
			if !ok {
				return nil, ErrPublishUncertain
			}
			if !ack.Ack {
				return nil, ErrNacked
			}
			select {
			case r := <-c.returns:
				if r.MessageId == o.id {
					return nil, ErrUnroutable
				}
			default:
			}
			return &PublishResult{MessageID: o.id, Queue: queue}, nil
		case <-ctx.Done():
			return nil, fmt.Errorf("%w: caller stopped waiting", ErrPublishUncertain)
		case <-t.C:
			return nil, ErrPublishUncertain
		}
	}
}
func (c *Client) Status() TransportStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	connected := c.conn != nil && !c.conn.IsClosed()
	s := TransportStatus{Driver: "rabbitmq", Connected: connected, Reconnecting: c.reconnecting.Load(), Generation: c.generation.Load(), Ready: connected && c.pub != nil && !c.pub.IsClosed() && !c.closed.Load() && !c.blocked.Load()}
	if v := c.lastErr.Load(); v != nil {
		s.LastError = v.(string)
	}
	return s
}
func (c *Client) Shutdown(ctx context.Context) error {
	c.shutdownOnce.Do(func() {
		c.closed.Store(true)
		go func() {
			c.mu.Lock()
			var errs []error
			if c.pub != nil {
				if err := c.pub.Close(); err != nil {
					errs = append(errs, fmt.Errorf("close publisher channel"))
				}
			}
			if c.conn != nil {
				if err := c.conn.Close(); err != nil {
					errs = append(errs, fmt.Errorf("close RabbitMQ connection"))
				}
			}
			c.shutdownErr = errors.Join(errs...)
			c.mu.Unlock()
			close(c.shutdownDone)
		}()
	})
	select {
	case <-c.shutdownDone:
		return c.shutdownErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Client) supervise(ctx context.Context, report func(error), logger *slog.Logger) {
	delay := c.cfg.RabbitMQ.Reconnect.InitialDelay
	for {
		select {
		case <-ctx.Done():
			return
		case blocked, ok := <-c.blockedNotify:
			if ok {
				c.blocked.Store(blocked.Active)
				if logger != nil {
					if blocked.Active {
						logger.WarnContext(ctx, "[MessageModule] RabbitMQ connection blocked", "reason", "broker resource alarm")
					} else {
						logger.InfoContext(ctx, "[MessageModule] RabbitMQ connection unblocked")
					}
				}
			}
		case brokerErr, ok := <-c.closedNotify:
			if ctx.Err() != nil || c.closed.Load() {
				return
			}
			if ok && brokerErr != nil && (brokerErr.Code == 403 || brokerErr.Code == 406) {
				report(&Error{Op: "connection closed", Reason: "RabbitMQ rejected the configured connection or topology"})
				return
			}
			c.reconnecting.Store(true)
			c.mu.Lock()
			if c.pub != nil {
				_ = c.pub.Close()
			}
			c.pub = nil
			c.conn = nil
			c.mu.Unlock()
			for {
				if c.closed.Load() || ctx.Err() != nil {
					return
				}
				wait := delay
				if c.cfg.RabbitMQ.Reconnect.Jitter != nil && *c.cfg.RabbitMQ.Reconnect.Jitter {
					wait = jitter(wait)
				}
				t := time.NewTimer(wait)
				select {
				case <-ctx.Done():
					t.Stop()
					return
				case <-t.C:
				}
				err := c.connect(ctx)
				if err == nil {
					break
				}
				var permanent *Error
				if errors.As(err, &permanent) && permanent.Permanent {
					report(err)
					return
				}
				c.lastErr.Store("RabbitMQ reconnect failed")
				if delay < c.cfg.RabbitMQ.Reconnect.MaxDelay {
					delay = time.Duration(float64(delay) * c.cfg.RabbitMQ.Reconnect.Multiplier)
					if delay > c.cfg.RabbitMQ.Reconnect.MaxDelay {
						delay = c.cfg.RabbitMQ.Reconnect.MaxDelay
					}
				}
			}
			delay = c.cfg.RabbitMQ.Reconnect.InitialDelay
			if c.closed.Load() {
				return
			}
			if c.started.Load() {
				if err := c.restartConsumers(); err != nil {
					report(err)
					return
				}
			}
			c.lastErr.Store("")
			if logger != nil {
				logger.InfoContext(ctx, "[MessageModule] RabbitMQ connection restored", "generation", c.generation.Load())
			}
		}
	}
}

func (c *Client) restartConsumers() error {
	c.consumersMu.Lock()
	regs := make([]*consumerRegistration, 0, len(c.consumers))
	for _, r := range c.consumers {
		regs = append(regs, r)
	}
	c.consumersMu.Unlock()
	for _, r := range regs {
		if r.done != nil {
			<-r.done
		}
	}
	c.consumersMu.Lock()
	for _, r := range regs {
		r.done = nil
		r.cancel = nil
	}
	c.consumersMu.Unlock()
	return c.startConsumers(c.lifeCtx, c.consumerLogger)
}
func (c *Client) Total(ctx context.Context, queue string) (QueueTotal, error) {
	if err := ctx.Err(); err != nil {
		return QueueTotal{}, err
	}
	q, ok := c.queues[queue]
	if !ok {
		return QueueTotal{}, ErrQueueNotRegistered
	}
	if e := c.usable(); e != nil {
		return QueueTotal{}, e
	}
	conn := c.connection()
	if conn == nil {
		return QueueTotal{}, ErrTransportUnavailable
	}
	ch, e := conn.Channel()
	if e != nil {
		return QueueTotal{}, ErrStatsUnavailable
	}
	defer ch.Close()
	main, e := ch.QueueDeclarePassive(queue, true, false, false, false, nil)
	if e != nil {
		return QueueTotal{}, ErrStatsUnavailable
	}
	t := QueueTotal{Queue: queue, Pending: messageCount(main.Messages), Known: Known{Pending: true}, IsEstimate: true}
	if q.deadLetter {
		x, e := ch.QueueDeclarePassive(queue+"_dlq", true, false, false, false, nil)
		if e != nil {
			return t, ErrStatsUnavailable
		}
		t.Failed = messageCount(x.Messages)
		t.Known.Failed = true
	}
	if q.retryDelay > 0 {
		x, e := ch.QueueDeclarePassive(queue+"_retry", true, false, false, false, nil)
		if e != nil {
			return t, ErrStatsUnavailable
		}
		t.Retrying = messageCount(x.Messages)
		t.Known.Retrying = true
	}
	return t, nil
}

func messageCount(n int) uint64 {
	if n <= 0 {
		return 0
	}
	return uint64(n)
}

func (c *Client) retryDelivery(d *Delivery, ctx context.Context) error {
	q := c.queues[d.Queue]
	if q.retryDelay <= 0 || d.Attempt >= q.maxAttempts || d.Attempt >= 1<<31-1 {
		return ErrNacked
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.usableLocked(); e != nil {
		return e
	}
	headers := amqp.Table{}
	for k, v := range d.raw.Headers {
		headers[k] = v
	}
	headers["x-nforge-attempt"] = int32(d.Attempt + 1)
	if e := c.pub.PublishWithContext(ctx, "", d.Queue+"_retry", true, false, amqp.Publishing{Headers: headers, ContentType: d.raw.ContentType, DeliveryMode: amqp.Persistent, Priority: d.raw.Priority, MessageId: d.raw.MessageId, Timestamp: time.Now(), Body: d.raw.Body}); e != nil {
		return ErrPublishUncertain
	}
	t := time.NewTimer(c.cfg.Publisher.ConfirmTimeout)
	defer t.Stop()
	for {
		select {
		case conf, ok := <-c.confirm:
			if !ok {
				return ErrPublishUncertain
			}
			if !conf.Ack {
				return ErrNacked
			}
			return nil
		case r := <-c.returns:
			if r.MessageId == d.ID {
				return ErrUnroutable
			}
		case <-ctx.Done():
			return ErrPublishUncertain
		case <-t.C:
			return ErrPublishUncertain
		}
	}
}

func (c *Client) Reprocess(ctx context.Context, queue string, limit int) (ReprocessResult, error) {
	q, ok := c.queues[queue]
	if !ok {
		return ReprocessResult{}, ErrQueueNotRegistered
	}
	if !q.deadLetter {
		return ReprocessResult{}, ErrUnsupportedFeature
	}
	if limit < 0 {
		return ReprocessResult{}, ErrInvalidLimit
	}
	if e := c.usable(); e != nil {
		return ReprocessResult{}, e
	}
	c.reprocessMu.Lock()
	if c.reprocessing[queue] {
		c.reprocessMu.Unlock()
		return ReprocessResult{}, ErrReprocessRunning
	}
	c.reprocessing[queue] = true
	c.reprocessMu.Unlock()
	defer func() { c.reprocessMu.Lock(); delete(c.reprocessing, queue); c.reprocessMu.Unlock() }()
	conn := c.connection()
	if conn == nil {
		return ReprocessResult{}, ErrTransportUnavailable
	}
	ch, e := conn.Channel()
	if e != nil {
		return ReprocessResult{}, ErrTransportUnavailable
	}
	defer ch.Close()
	if limit == 0 {
		s, e := ch.QueueDeclarePassive(queue+"_dlq", true, false, false, false, nil)
		if e != nil {
			return ReprocessResult{}, ErrStatsUnavailable
		}
		limit = s.Messages
	}
	var out ReprocessResult
	for i := 0; i < limit; i++ {
		if e := ctx.Err(); e != nil {
			return out, e
		}
		got, ok, e := ch.Get(queue+"_dlq", false)
		if e != nil {
			return out, ErrStatsUnavailable
		}
		if !ok {
			break
		}
		headers := got.Headers
		if headers == nil {
			headers = amqp.Table{}
		}
		delete(headers, "x-nforge-attempt")
		headers["x-nforge-reprocessed"] = time.Now().UTC().Format(time.RFC3339Nano)
		c.mu.Lock()
		e = c.pub.PublishWithContext(ctx, "", queue, true, false, amqp.Publishing{Headers: headers, ContentType: got.ContentType, DeliveryMode: amqp.Persistent, Priority: got.Priority, MessageId: got.MessageId, Timestamp: time.Now(), Body: got.Body})
		if e == nil {
			timer := time.NewTimer(c.cfg.Publisher.ConfirmTimeout)
			done := false
			for !done {
				select {
				case conf, ok := <-c.confirm:
					if !ok {
						e = ErrPublishUncertain
					} else if !conf.Ack {
						e = ErrNacked
					}
					done = true
				case r := <-c.returns:
					if r.MessageId == got.MessageId {
						e = ErrUnroutable
						done = true
					}
				case <-ctx.Done():
					e = ErrPublishUncertain
					done = true
				case <-timer.C:
					e = ErrPublishUncertain
					done = true
				}
			}
			timer.Stop()
		}
		c.mu.Unlock()
		if e != nil {
			_ = ch.Nack(got.DeliveryTag, false, true)
			out.Failed++
			continue
		}
		if e = ch.Ack(got.DeliveryTag, false); e != nil {
			out.Failed++
			continue
		}
		out.Reprocessed++
	}
	return out, nil
}

type QueueTotal struct {
	Queue      string  // queue queried
	Pending    uint64  // ready in the main queue
	Processing *uint64 // unacknowledged deliveries; nil when unavailable
	Retrying   uint64  // ready in the retry queue
	Failed     uint64  // ready in the DLQ
	Known      Known   // identifies which fields were measured
	IsEstimate bool    // true because AMQP snapshots are non-atomic
}

// Known marks which fields of QueueTotal were measured.
type Known struct {
	Pending    bool // pending count is known
	Processing bool // in-flight count is known
	Retrying   bool // retry queue count is known
	Failed     bool // DLQ count is known
}

// ReprocessResult counts successfully reprocessed and failed DLQ messages.
type ReprocessResult struct {
	Reprocessed uint64 // republished and acknowledged from the DLQ
	Failed      uint64 // could not be confirmed for republishing
}

// TransportStatus is a sanitized, local snapshot of the RabbitMQ connection.
type TransportStatus struct {
	Driver       string // transport name
	Ready        bool   // connection and publisher channel are available
	Connected    bool   // connection state
	Reconnecting bool   // reconnect supervisor is active
	Generation   uint64 // increments for each established connection
	LastError    string // sanitized error text
}

// random jitter helper kept local to avoid global random state.
func jitter(d time.Duration) time.Duration {
	n, _ := rand.Int(rand.Reader, big.NewInt(int64(d/5+1)))
	return d + time.Duration(n.Int64())
}
