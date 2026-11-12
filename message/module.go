package message

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/ams-soft/nforge/codec"
	"github.com/ams-soft/nforge/metrics"
	"github.com/ams-soft/nforge/module"
	"github.com/ams-soft/nforge/telemetry"
	amqp "github.com/rabbitmq/amqp091-go"
)

type moduleOptions struct{ queues []QueueSpec }

// ModuleOption configures the message module.
type ModuleOption func(*moduleOptions)

// WithQueues adds programmatic queue declarations.
func WithQueues(queues ...QueueSpec) ModuleOption {
	return func(o *moduleOptions) { o.queues = append(o.queues, queues...) }
}

// Module declares the RabbitMQ connection resource. Register Consumers separately to receive messages.
func Module(opts ...ModuleOption) module.Descriptor {
	o := moduleOptions{}
	for _, f := range opts {
		if f != nil {
			f(&o)
		}
	}
	return module.Descriptor{ID: "message", Section: "message", Configure: func(src module.Source) (module.Definition, error) {
		var cfg Config
		if e := src.Decode(&cfg); e != nil {
			return module.Definition{}, e
		}
		cfg.defaults()
		if cfg.RabbitMQ.ConnectionName == "" {
			cfg.RabbitMQ.ConnectionName = src.App().Name
		}
		if !cfg.Enabled {
			return module.Definition{Disabled: true, Provides: []module.Handle{Capability}, Requires: []module.Handle{codec.Capability}, Optional: []module.Handle{module.Logger, telemetry.Capability}, Config: cfg}, nil
		}
		if cfg.Enabled {
			if e := cfg.validate(); e != nil {
				return module.Definition{}, e
			}
			if cfg.plaintextCredentials() {
				src.Warn("message_plaintext", "message.rabbitmq sends credentials without TLS to a host that is not local")
			}
		}
		queues := make([]QueueSpec, 0, len(cfg.Queues)+len(o.queues))
		seen := map[string]QueueSpec{}
		for _, qc := range cfg.Queues {
			q := qc.spec()
			seen[q.name] = q
			queues = append(queues, q)
		}
		for _, q := range o.queues {
			if e := q.validate(); e != nil {
				return module.Definition{}, e
			}
			if old, ok := seen[q.name]; ok {
				if !equalQueue(old, q) {
					return module.Definition{}, fmt.Errorf("%w: queue %s differs between YAML and code", ErrQueueConflict, q.name)
				}
				continue
			}
			seen[q.name] = q
			queues = append(queues, q)
		}
		return module.Definition{Disabled: !cfg.Enabled, Provides: []module.Handle{Capability}, Requires: []module.Handle{codec.Capability}, Optional: []module.Handle{module.Logger, telemetry.Capability, metrics.Capability}, Config: cfg, Check: func(v *module.View) error {
			cs, _, _, e := module.ConfigOf[codec.Settings](v, codec.Capability)
			if e != nil {
				return e
			}
			if !cs.Enables(cfg.PayloadFormat) {
				return fmt.Errorf("message.payload_format %q must be listed in codec.formats.enabled", cfg.PayloadFormat)
			}
			if int64(cfg.Publisher.MaxPayloadBytes) > int64(cs.MaxPayloadBytes) {
				return fmt.Errorf("message.publisher.max_payload_bytes exceeds codec.max_payload_bytes")
			}
			return nil
		}, Init: func(ctx context.Context, sc *module.Scope) (module.Hooks, error) {
			if !cfg.Enabled {
				return module.Hooks{}, nil
			}
			startup, cancel := context.WithTimeout(ctx, cfg.RabbitMQ.DialTimeout)
			defer cancel()
			var logger *slog.Logger
			logger, _ = module.Lookup(sc, module.Logger)
			cl, e := openClient(startup, cfg, module.Require(sc, codec.Capability), logger, queues)
			if e != nil {
				return module.Hooks{}, e
			}
			if tr, ok := module.Lookup(sc, telemetry.Capability); ok && tr != nil {
				cl.tracer = tr.Provider.Tracer("github.com/ams-soft/nforge/message")
				cl.trustIncoming = tr.TrustIncoming
			}
			module.Provide(sc, Capability, cl)
			if reg, ok := module.Lookup(sc, metrics.Capability); ok {
				cl.registerMetrics(reg)
			}
			cl.lifeCtx = sc.Context()
			cl.failure = sc.ReportFailure
			cl.consumerLogger = logger
			go cl.supervise(sc.Context(), sc.ReportFailure, logger)
			if logger != nil {
				logger.InfoContext(ctx, "[MessageModule] RabbitMQ connection ready", "driver", "rabbitmq", "queues", len(queues))
			}
			return module.Hooks{Start: func(context.Context) error { cl.started.Store(true); return nil }, Ready: func(context.Context) error {
				if cl.Status().Ready {
					return nil
				}
				return ErrTransportUnavailable
			}, Stop: cl.Shutdown}, nil
		}}, nil
	}}
}

type consumerOptions struct {
	concurrency, prefetch int
	timeout               time.Duration
	manual                bool
}

// ConsumerOption changes one consumer's worker behavior.
type ConsumerOption func(*consumerOptions) error

// WithConcurrency sets the number of workers.
func WithConcurrency(n int) ConsumerOption {
	return func(o *consumerOptions) error {
		if n < 1 || n > 1024 {
			return fmt.Errorf("message: concurrency must be between 1 and 1024")
		}
		o.concurrency = n
		return nil
	}
}

// WithPrefetch sets a bounded broker prefetch; zero selects worker concurrency.
func WithPrefetch(n int) ConsumerOption {
	return func(o *consumerOptions) error {
		if n < 0 || n > 65535 {
			return fmt.Errorf("message: prefetch must be between 0 and 65535")
		}
		o.prefetch = n
		return nil
	}
}

// WithTimeout bounds each handler call.
func WithTimeout(d time.Duration) ConsumerOption {
	return func(o *consumerOptions) error {
		if d <= 0 {
			return fmt.Errorf("message: timeout must be positive")
		}
		o.timeout = d
		return nil
	}
}

// WithManualAck makes handlers responsible for settling each delivery.
func WithManualAck() ConsumerOption {
	return func(o *consumerOptions) error { o.manual = true; return nil }
}

type consumerRegistration struct {
	queue   string
	handler Handler
	opts    consumerOptions
	cancel  func() error
	done    chan struct{}
}

// Handler processes one RabbitMQ delivery.
type Handler func(context.Context, *Delivery) error

// AddConsumer registers one consumer during module initialization.
func (c *Client) AddConsumer(queue string, handler Handler, opts ...ConsumerOption) error {
	if c.started.Load() {
		return ErrStarted
	}
	if handler == nil {
		return fmt.Errorf("message: handler is required")
	}
	q, ok := c.queues[queue]
	if !ok {
		return ErrQueueNotRegistered
	}
	if !q.deadLetter && !q.dropOnFailure && c.logger != nil {
		c.logger.Warn("[MessageModule] consumer on a queue without dead_letter: a message whose handler fails is discarded; declare dead_letter or set on_failure: drop", "queue", queue)
	}
	o := consumerOptions{concurrency: c.cfg.Consumer.Concurrency, prefetch: c.cfg.Consumer.Prefetch, timeout: c.cfg.Consumer.Timeout, manual: c.cfg.Consumer.AckMode == "manual"}
	for _, f := range opts {
		if e := f(&o); e != nil {
			return e
		}
	}
	c.consumersMu.Lock()
	defer c.consumersMu.Unlock()
	if c.started.Load() {
		return ErrStarted
	}
	if _, ok := c.consumers[queue]; ok {
		return fmt.Errorf("message: consumer already registered for queue %s", queue)
	}
	c.consumers[queue] = &consumerRegistration{queue: queue, handler: handler, opts: o}
	return nil
}

// Consumers declares message consumers as ingress, so shutdown drains them before the connection closes.
func Consumers() module.Descriptor {
	return module.Descriptor{ID: "message-consumers", Configure: func(module.Source) (module.Definition, error) {
		return module.Definition{Optional: []module.Handle{Capability, module.Logger}, Ingress: true, Init: func(ctx context.Context, sc *module.Scope) (module.Hooks, error) {
			cl, enabled := module.Lookup(sc, Capability)
			logger, _ := module.Lookup(sc, module.Logger)
			if !enabled {
				return module.Hooks{}, nil
			}
			return module.Hooks{Start: func(context.Context) error { return cl.startConsumers(cl.lifeCtx, logger) }, Stop: cl.stopConsumers}, nil
		}}, nil
	}}
}

func (c *Client) startConsumers(ctx context.Context, logger *slog.Logger) error {
	c.consumersMu.Lock()
	defer c.consumersMu.Unlock()
	conn := c.connection()
	if conn == nil || conn.IsClosed() {
		return ErrTransportUnavailable
	}
	c.started.Store(true)
	for _, r := range c.consumers {
		ch, e := conn.Channel()
		if e != nil {
			return ErrTransportUnavailable
		}
		prefetch := r.opts.prefetch
		if prefetch == 0 {
			prefetch = r.opts.concurrency
		}
		if e = ch.Qos(prefetch, 0, false); e != nil {
			_ = ch.Close()
			return ErrTransportUnavailable
		}
		consumerTag := "nforge-" + r.queue
		deliveries, e := ch.Consume(r.queue, consumerTag, false, false, false, false, nil)
		if e != nil {
			_ = ch.Close()
			return fmt.Errorf("message: start consumer for %s", r.queue)
		}
		done := make(chan struct{})
		r.done = done
		r.cancel = func() error { return ch.Cancel(consumerTag, false) }
		workers := make(chan amqp.Delivery, prefetch)
		c.consWG.Add(1)
		go func() {
			defer c.consWG.Done()
			defer close(done)
			var wg sync.WaitGroup
			for i := 0; i < r.opts.concurrency; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for raw := range workers {
						d := newDelivery(c, r, raw)
						hctx, cancel := context.WithTimeout(ctx, r.opts.timeout)
						hctx, endSpan := c.consumerContext(hctx, r.queue, raw.Headers)
						panicStack, err := safeHandle(r.handler, hctx, d)
						endSpan(err)
						timed := hctx.Err() != nil
						c.stats.consumed.Add(1)
						if err != nil || timed {
							c.stats.failed.Add(1)
						}
						if r.opts.manual {
							if !d.settled() {
								_ = d.Nack(false)
								if logger != nil {
									logger.WarnContext(hctx, "[MessageModule] manual handler returned without settlement", "queue", r.queue, "message_id", d.ID)
								}
							} else if err != nil && logger != nil {
								logger.ErrorContext(hctx, "[MessageModule] handler failed after settlement", "queue", r.queue, "message_id", d.ID, "error", "handler error")
							}
						} else if err == nil && !timed {
							_ = d.Ack()
						} else {
							if !timed && c.retryDelivery(d, hctx) == nil {
								c.stats.retried.Add(1)
								_ = d.Ack()
							} else {
								c.stats.dropped.Add(1)
								_ = d.Nack(false)
							}
							if logger != nil {
								if panicStack != nil {
									logger.ErrorContext(hctx, "[MessageModule] consumer handler panicked", "queue", r.queue, "message_id", d.ID, "stack", string(panicStack))
								} else {
									logger.WarnContext(hctx, "[MessageModule] message rejected", "queue", r.queue, "message_id", d.ID)
								}
							}
						}
						cancel()
					}
				}()
			}
			for raw := range deliveries {
				workers <- raw
			}
			close(workers)
			wg.Wait()
		}()
	}
	return nil
}
func (c *Client) stopConsumers(ctx context.Context) error {
	c.consumersMu.Lock()
	regs := make([]*consumerRegistration, 0, len(c.consumers))
	for _, r := range c.consumers {
		regs = append(regs, r)
		if r.cancel != nil {
			_ = r.cancel()
		}
	}
	c.consumersMu.Unlock()
	waited := make(chan struct{})
	go func() { c.consWG.Wait(); close(waited) }()
	select {
	case <-waited:
	case <-ctx.Done():
		return ctx.Err()
	}
	_ = regs
	return nil
}

type Delivery struct {
	ID           string            // message ID
	Queue        string            // source queue
	Body         []byte            // message body, bounded by publisher.max_payload_bytes
	ContentType  string            // AMQP content type
	Headers      map[string]string // user headers only
	Priority     uint8             // native Classic priority
	Redelivered  bool              // broker redelivery indicator
	Attempt      uint32            // best-effort transport retry count
	raw          amqp.Delivery
	client       *Client
	registration *consumerRegistration
	generation   uint64
	settleMu     sync.Mutex
	settledFlag  bool
}

func newDelivery(c *Client, r *consumerRegistration, d amqp.Delivery) *Delivery {
	h := map[string]string{}
	for k, v := range d.Headers {
		if s, ok := v.(string); ok && !strings.HasPrefix(strings.ToLower(k), "x-") {
			h[k] = s
		}
	}
	n, _ := d.Headers["x-nforge-attempt"].(int32)
	return &Delivery{ID: d.MessageId, Queue: r.queue, Body: append([]byte(nil), d.Body...), ContentType: d.ContentType, Headers: h, Priority: d.Priority, Redelivered: d.Redelivered, Attempt: uint32(max(0, n)), raw: d, client: c, registration: r, generation: c.generation.Load()}
}
func (d *Delivery) Decode(dst any) error {
	if int64(len(d.Body)) > int64(d.client.cfg.Publisher.MaxPayloadBytes) {
		return ErrPayloadTooLarge
	}
	f := d.raw.Headers["x-nforge-format"]
	if f == nil {
		return fmt.Errorf("message: raw delivery has no codec format")
	}
	format, ok := f.(string)
	if !ok {
		return fmt.Errorf("message: invalid format")
	}
	return d.client.registry.DecodeAs(format, d.Body, dst)
}
func (d *Delivery) settled() bool { d.settleMu.Lock(); defer d.settleMu.Unlock(); return d.settledFlag }
func (d *Delivery) settle(ack, requeue bool) error {
	d.settleMu.Lock()
	defer d.settleMu.Unlock()
	if d.settledFlag {
		return ErrAlreadySettled
	}
	if d.client.generation.Load() != d.generation {
		return ErrStaleDelivery
	}
	d.settledFlag = true
	if ack {
		return d.raw.Ack(false)
	}
	return d.raw.Nack(false, requeue)
}
func (d *Delivery) Ack() error              { return d.settle(true, false) }
func (d *Delivery) Nack(requeue bool) error { return d.settle(false, requeue) }
func safeHandle(h Handler, ctx context.Context, d *Delivery) (panicStack []byte, err error) {
	defer func() {
		if recover() != nil {
			err = fmt.Errorf("message: handler panic")
			panicStack = debug.Stack()
		}
	}()
	err = h(ctx, d)
	return nil, err
}
