package message

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"sync/atomic"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// Fanout exchanges carry best-effort events that every live subscriber must
// see: no durable queue, no retry, no dead-letter. They reuse the client's one
// connection, publisher channel and reconnect supervisor, so a module that
// needs them never opens a second RabbitMQ client.

var exchangePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func validExchange(name string) error {
	if !exchangePattern.MatchString(name) || len(name) > 255 {
		return &Error{Op: "fanout", Reason: "invalid exchange name"}
	}
	return nil
}

// PublishFanout publishes body to the named fanout exchange and waits for the
// broker's publisher confirm. The exchange is declared (idempotently) on every
// connection generation, so it survives a broker restart. The message is
// transient and not mandatory: with no bound subscriber the broker drops it and
// the publish still succeeds, which is the "nobody is listening" case of a
// best-effort fanout. A broker outage is ErrTransportUnavailable; a lost
// confirm is ErrPublishUncertain. No exactly-once guarantee exists.
func (c *Client) PublishFanout(ctx context.Context, exchange string, body []byte) error {
	return c.publishExchange(ctx, exchange, "fanout", "", body)
}

func (c *Client) publishExchange(ctx context.Context, exchange, kind, key string, body []byte) error {
	if err := validExchange(exchange); err != nil {
		return err
	}
	if int64(len(body)) > int64(c.cfg.Publisher.MaxPayloadBytes) {
		return ErrPayloadTooLarge
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	if err := c.usableLocked(); err != nil {
		c.mu.Unlock()
		return err
	}
	conn, gen := c.conn, c.generation.Load()
	c.mu.Unlock()
	c.fanoutMu.Lock()
	if c.fanoutDeclared == nil || c.fanoutGeneration != gen {
		c.fanoutDeclared = map[string]bool{}
		c.fanoutGeneration = gen
	}
	if !c.fanoutDeclared[exchange] {
		if err := declareExchange(conn, exchange, kind); err != nil {
			c.fanoutMu.Unlock()
			return fmt.Errorf("%w: fanout exchange unavailable", ErrTransportUnavailable)
		}
		c.fanoutDeclared[exchange] = true
	}
	c.fanoutMu.Unlock()
	return c.publishConfirmed(ctx, exchange, key, false, amqp.Publishing{ContentType: "application/octet-stream", DeliveryMode: amqp.Transient, Timestamp: time.Now(), Body: body})
}

func declareExchange(conn *amqp.Connection, exchange, kind string) error {
	ch, err := conn.Channel()
	if err != nil {
		return err
	}
	defer ch.Close()
	return ch.ExchangeDeclare(exchange, kind, true, false, false, false, nil)
}

// FanoutHandler processes one message of a fanout subscription. Returning nil
// acknowledges it; an error rejects it without requeue, so a poison message is
// dropped once and never loops.
type FanoutHandler func(ctx context.Context, body []byte) error

// FanoutOption configures a fanout subscription.
type FanoutOption func(*fanoutOptions)

type fanoutOptions struct {
	prefetch int
	timeout  time.Duration
	maxBytes int
	onState  func(active bool)
}

// WithFanoutPrefetch bounds the unacknowledged deliveries of the subscription
// (default 16). One worker processes them in order, so this is also the
// buffer the broker may fill ahead of the handler.
func WithFanoutPrefetch(n int) FanoutOption { return func(o *fanoutOptions) { o.prefetch = n } }

// WithFanoutTimeout bounds one handler call (default 5s).
func WithFanoutTimeout(d time.Duration) FanoutOption { return func(o *fanoutOptions) { o.timeout = d } }

// WithFanoutMaxBytes rejects, without calling the handler, messages larger
// than n bytes (default: publisher.max_payload_bytes).
func WithFanoutMaxBytes(n int) FanoutOption { return func(o *fanoutOptions) { o.maxBytes = n } }

// WithFanoutState reports every change of the subscription's attachment: true
// once its queue is bound and consuming, false when the connection is lost.
func WithFanoutState(f func(active bool)) FanoutOption {
	return func(o *fanoutOptions) { o.onState = f }
}

// Subscription is an ephemeral per-instance subscription to a fanout exchange:
// an exclusive, auto-delete, non-durable, server-named queue bound to it. After
// a broker outage the subscription creates a new queue and binds it again by
// itself, with the client's reconnect backoff; messages published meanwhile
// are lost by design.
type Subscription struct {
	c        *Client
	exchange string
	handler  FanoutHandler
	opts     fanoutOptions
	ctx      context.Context
	cancel   context.CancelFunc
	done     chan struct{}
	active   atomic.Bool
	closeMu  sync.Mutex
	ch       *amqp.Channel
	attaches atomic.Uint64
	rejected atomic.Uint64

	// Routed subscriptions (direct exchange, bindings per key).
	kind    string // exchange type: fanout or direct
	applyMu sync.Mutex
	keys    map[string]struct{} // routing keys that must be bound; guarded by applyMu
	queue   string              // current queue name; guarded by applyMu
}

// SubscribeFanout attaches to the fanout exchange and starts consuming. It
// returns once the first attachment succeeded, so a caller can open its own
// ingress after it. The client must be started.
func (c *Client) SubscribeFanout(ctx context.Context, exchange string, h FanoutHandler, opts ...FanoutOption) (*Subscription, error) {
	return c.subscribe(exchange, "fanout", h, opts)
}

// SubscribeRouted is SubscribeFanout on a direct exchange: the instance's
// ephemeral queue starts with no bindings and receives only the messages
// published with a routing key it has bound (Sync). The bindings are kept, so
// after an outage the new queue is bound again with every key still wanted.
// It is how an instance receives the events of the users it actually serves
// instead of every event of every user.
func (c *Client) SubscribeRouted(ctx context.Context, exchange string, h FanoutHandler, opts ...FanoutOption) (*Subscription, error) {
	return c.subscribe(exchange, "direct", h, opts)
}

func (c *Client) subscribe(exchange, kind string, h FanoutHandler, opts []FanoutOption) (*Subscription, error) {
	if err := validExchange(exchange); err != nil {
		return nil, err
	}
	if h == nil {
		return nil, &Error{Op: "fanout", Reason: "nil handler"}
	}
	if err := c.usable(); err != nil {
		return nil, err
	}
	o := fanoutOptions{prefetch: 16, timeout: 5 * time.Second, maxBytes: int(c.cfg.Publisher.MaxPayloadBytes)}
	for _, f := range opts {
		f(&o)
	}
	if o.prefetch < 1 || o.timeout <= 0 || o.maxBytes < 1 {
		return nil, &Error{Op: "fanout", Reason: "invalid subscription options"}
	}
	base := c.lifeCtx
	if base == nil {
		base = context.Background()
	}
	sctx, cancel := context.WithCancel(base)
	s := &Subscription{c: c, exchange: exchange, handler: h, opts: o, ctx: sctx, cancel: cancel, done: make(chan struct{}), kind: kind, keys: map[string]struct{}{}}
	deliveries, err := s.attach()
	if err != nil {
		cancel()
		return nil, err
	}
	go s.run(deliveries)
	return s, nil
}

// Active reports whether the subscription's queue is bound and consuming.
func (s *Subscription) Active() bool { return s.active.Load() }

// Attachments counts the successful attachments, the first one included; a
// value above 1 means the subscription was restored after an outage.
func (s *Subscription) Attachments() uint64 { return s.attaches.Load() }

// Rejected counts the messages dropped for size or because the handler failed.
func (s *Subscription) Rejected() uint64 { return s.rejected.Load() }

// Close stops consuming, waits for the handler in flight (bounded by ctx) and
// deletes the queue by closing its channel. It is idempotent.
func (s *Subscription) Close(ctx context.Context) error {
	s.cancel()
	s.closeMu.Lock()
	if s.ch != nil {
		_ = s.ch.Close()
	}
	s.closeMu.Unlock()
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Subscription) setActive(v bool) {
	if s.active.Swap(v) != v && s.opts.onState != nil {
		s.opts.onState(v)
	}
}

// attach declares the exchange and a fresh queue on the current connection,
// binds them and starts consuming.
func (s *Subscription) attach() (<-chan amqp.Delivery, error) {
	conn := s.c.connection()
	if conn == nil || conn.IsClosed() {
		return nil, ErrTransportUnavailable
	}
	ch, err := conn.Channel()
	if err != nil {
		return nil, fmt.Errorf("%w: subscription channel unavailable", ErrTransportUnavailable)
	}
	fail := func(what string) (<-chan amqp.Delivery, error) {
		_ = ch.Close()
		return nil, fmt.Errorf("%w: fanout %s failed", ErrTransportUnavailable, what)
	}
	if err = ch.ExchangeDeclare(s.exchange, s.kind, true, false, false, false, nil); err != nil {
		return fail("exchange declaration")
	}
	q, err := ch.QueueDeclare("", false, true, true, false, nil)
	if err != nil {
		return fail("queue declaration")
	}
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	if s.kind == "fanout" {
		if err = ch.QueueBind(q.Name, "", s.exchange, false, nil); err != nil {
			return fail("binding")
		}
	} else {
		for key := range s.keys { // every key still wanted, on the new queue
			if err = ch.QueueBind(q.Name, key, s.exchange, false, nil); err != nil {
				return fail("binding")
			}
		}
	}
	if err = ch.Qos(s.opts.prefetch, 0, false); err != nil {
		return fail("qos")
	}
	deliveries, err := ch.Consume(q.Name, "", false, true, false, false, nil)
	if err != nil {
		return fail("consume")
	}
	s.closeMu.Lock()
	s.ch = ch
	s.closeMu.Unlock()
	s.queue = q.Name
	s.attaches.Add(1)
	s.setActive(true)
	return deliveries, nil
}

// run consumes until closed, re-attaching with the client's backoff whenever
// the deliveries channel ends (connection or channel loss).
func (s *Subscription) run(deliveries <-chan amqp.Delivery) {
	defer close(s.done)
	defer s.setActive(false)
	rc := s.c.cfg.RabbitMQ.Reconnect
	delay := rc.InitialDelay
	for {
		s.consume(deliveries)
		s.setActive(false)
		for {
			if s.ctx.Err() != nil || s.c.closed.Load() {
				return
			}
			wait := delay
			if rc.Jitter != nil && *rc.Jitter {
				wait = jitter(wait)
			}
			t := time.NewTimer(wait)
			select {
			case <-s.ctx.Done():
				t.Stop()
				return
			case <-t.C:
			}
			d, err := s.attach()
			if err == nil {
				deliveries, delay = d, rc.InitialDelay
				break
			}
			if delay = time.Duration(float64(delay) * rc.Multiplier); delay > rc.MaxDelay {
				delay = rc.MaxDelay
			}
		}
	}
}

func (s *Subscription) consume(deliveries <-chan amqp.Delivery) {
	for {
		select {
		case <-s.ctx.Done():
			return
		case d, ok := <-deliveries:
			if !ok {
				return
			}
			s.handle(d)
		}
	}
}

func (s *Subscription) handle(d amqp.Delivery) {
	if len(d.Body) > s.opts.maxBytes {
		s.rejected.Add(1)
		_ = d.Nack(false, false)
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, s.opts.timeout)
	err := s.safeHandle(ctx, d.Body)
	cancel()
	if err != nil {
		s.rejected.Add(1)
		_ = d.Nack(false, false) // no requeue: a poison message must not loop
		return
	}
	_ = d.Ack(false)
}

func (s *Subscription) safeHandle(ctx context.Context, body []byte) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("message: fanout handler panicked")
		}
	}()
	return s.handler(ctx, body)
}

// Sync binds or unbinds the routing key of a routed subscription so that it
// matches want() (evaluated under the subscription's apply lock). Because the
// state is read at apply time, concurrent Syncs of one key converge on the
// latest truth whatever order they run in. The wanted set is remembered even
// when the broker call fails (outage): the next attachment binds it.
func (s *Subscription) Sync(key string, want func() bool) error {
	if s.kind != "direct" {
		return &Error{Op: "fanout", Reason: "Sync needs a routed subscription"}
	}
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	wanted := want()
	if wanted {
		s.keys[key] = struct{}{}
	} else {
		delete(s.keys, key)
	}
	s.closeMu.Lock()
	ch, queue := s.ch, s.queue
	s.closeMu.Unlock()
	if ch == nil || ch.IsClosed() || queue == "" {
		return ErrTransportUnavailable
	}
	var err error
	if wanted {
		err = ch.QueueBind(queue, key, s.exchange, false, nil)
	} else {
		err = ch.QueueUnbind(queue, key, s.exchange, nil)
	}
	if err != nil {
		// a channel error closes the channel: the run loop re-attaches and rebinds from keys
		return fmt.Errorf("%w: binding change failed", ErrTransportUnavailable)
	}
	return nil
}

// PublishRouted publishes body to a direct exchange with a routing key and waits
// for the broker's confirm. Like PublishFanout it is transient and not
// mandatory: with no queue bound to the key the broker drops the message and the
// publish still succeeds (nobody is listening).
func (c *Client) PublishRouted(ctx context.Context, exchange, key string, body []byte) error {
	return c.publishExchange(ctx, exchange, "direct", key, body)
}
