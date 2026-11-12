//go:build integration

package message

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ams-soft/nforge/codec"
	amqp "github.com/rabbitmq/amqp091-go"
)

// netProxy forwards TCP to the broker, can delay the broker->client direction
// (a slow network) and can cut every connection (an outage).
type netProxy struct {
	t      testing.TB
	target string
	addr   string
	delay  atomic.Int64 // nanoseconds
	mu     sync.Mutex
	ln     net.Listener
	conns  []net.Conn
}

func newNetProxy(t testing.TB, brokerURL string, delay time.Duration) *netProxy {
	t.Helper()
	u, err := url.Parse(brokerURL)
	if err != nil {
		t.Fatal(err)
	}
	host := u.Host
	if u.Port() == "" {
		host += ":5672"
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &netProxy{t: t, target: host, addr: ln.Addr().String(), ln: ln}
	p.delay.Store(int64(delay))
	go p.serve(ln)
	t.Cleanup(p.cut)
	return p
}

func (p *netProxy) url(brokerURL string) string {
	u, _ := url.Parse(brokerURL)
	u.Host = p.addr
	return u.String()
}

func (p *netProxy) serve(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		up, err := net.Dial("tcp", p.target)
		if err != nil {
			_ = c.Close()
			continue
		}
		p.mu.Lock()
		p.conns = append(p.conns, c, up)
		p.mu.Unlock()
		go func() { _, _ = io.Copy(up, c); _ = up.Close() }()
		go func() {
			type chunk struct {
				at time.Time
				b  []byte
			}
			q := make(chan chunk, 8192)
			go func() {
				for ch := range q {
					if d := time.Duration(p.delay.Load()); d > 0 {
						time.Sleep(time.Until(ch.at.Add(d)))
					}
					_, _ = c.Write(ch.b)
				}
				_ = c.Close()
			}()
			buf := make([]byte, 32<<10)
			for {
				n, err := up.Read(buf)
				if n > 0 {
					q <- chunk{time.Now(), append([]byte(nil), buf[:n]...)}
				}
				if err != nil {
					close(q)
					return
				}
			}
		}()
	}
}

// cut closes the listener and every connection; restore listens again on the same port.
func (p *netProxy) cut() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ln != nil {
		_ = p.ln.Close()
		p.ln = nil
	}
	for _, c := range p.conns {
		_ = c.Close()
	}
	p.conns = nil
}

func (p *netProxy) restore() {
	var ln net.Listener
	var err error
	for range 100 {
		if ln, err = net.Listen("tcp", p.addr); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		p.t.Fatal(err)
	}
	p.mu.Lock()
	p.ln = ln
	p.mu.Unlock()
	go p.serve(ln)
}

func brokerURL(t testing.TB) string {
	t.Helper()
	raw := os.Getenv("NFORGE_TEST_RABBITMQ_URL")
	if raw == "" {
		t.Skip("NFORGE_TEST_RABBITMQ_URL is unset")
	}
	return raw
}

// clientVia opens a client whose connection goes through url (a proxy).
func clientVia(t *testing.T, raw string, mutate func(*Config), specs ...QueueSpec) *Client {
	t.Helper()
	reg, err := codec.New(codec.Config{Formats: []string{"json"}, Default: "json", MaxPayloadBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Enabled: true, Driver: "rabbitmq", Namespace: fmt.Sprintf("nf_test_%d", time.Now().UnixNano()), PayloadFormat: "json", RabbitMQ: RabbitMQConfig{URL: raw, Reconnect: ReconnectConfig{InitialDelay: 50 * time.Millisecond, MaxDelay: 200 * time.Millisecond, Multiplier: 2}}, Publisher: PublisherConfig{MaxPayloadBytes: codec.MB, ConfirmTimeout: 5 * time.Second}}
	cfg.defaults()
	if mutate != nil {
		mutate(&cfg)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	c, err := openClient(ctx, cfg, reg, nil, specs)
	if err != nil {
		t.Fatal(err)
	}
	c.started.Store(true)
	t.Cleanup(func() {
		_ = c.Shutdown(context.Background())
		if co, e := amqp.Dial(os.Getenv("NFORGE_TEST_RABBITMQ_URL")); e == nil {
			if ch, e := co.Channel(); e == nil {
				for _, q := range specs {
					_, _ = ch.QueueDelete(q.name+"_dlq", false, false, false)
					_, _ = ch.QueueDelete(q.name+"_retry", false, false, false)
					_, _ = ch.QueueDelete(q.name, false, false, false)
				}
				_ = ch.ExchangeDelete(cfg.Namespace+".dlx", false, false)
			}
			_ = co.Close()
		}
	})
	return c
}

// Regression (B1): a confirmation that arrives after a publish gave up must not
// be read by the next publish as its own.
func TestStaleConfirmationDoesNotSatisfyTheNextPublish(t *testing.T) {
	raw := brokerURL(t)
	p := newNetProxy(t, raw, 0)
	sfx := time.Now().UnixNano()
	okQ, goneQ := fmt.Sprintf("nf_ok_%d", sfx), fmt.Sprintf("nf_gone_%d", sfx)
	c := clientVia(t, p.url(raw), nil, Queue(okQ), Queue(goneQ))
	p.delay.Store(int64(250 * time.Millisecond))

	co, err := amqp.Dial(raw)
	if err != nil {
		t.Fatal(err)
	}
	defer co.Close()
	ch, _ := co.Channel()
	if _, err := ch.QueueDelete(goneQ, false, false, false); err != nil {
		t.Fatal(err)
	}

	ctxA, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	_, errA := c.Publish(ctxA, okQ, []byte("A"))
	cancel()
	if !errors.Is(errA, ErrPublishUncertain) {
		t.Fatalf("A (caller gave up) = %v, want ErrPublishUncertain", errA)
	}
	if _, errB := c.Publish(context.Background(), goneQ, []byte("B")); !errors.Is(errB, ErrUnroutable) {
		t.Fatalf("B (unroutable) = %v, want ErrUnroutable: a stale confirmation was taken as its own", errB)
	}
}

// Regression (B1): the number of successful publishes equals the messages that
// reached the queue, whatever the callers' deadlines.
func TestSuccessfulPublishesAreExactlyTheQueuedMessages(t *testing.T) {
	raw := brokerURL(t)
	p := newNetProxy(t, raw, 20*time.Millisecond)
	q := fmt.Sprintf("nf_exact_%d", time.Now().UnixNano())
	c := clientVia(t, p.url(raw), nil, Queue(q))
	var ok atomic.Int64
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Go(func() {
			for i := range 40 {
				ctx, cancel := context.WithTimeout(context.Background(), time.Duration(5+(g*7+i*3)%60)*time.Millisecond)
				if _, err := c.Publish(ctx, q, []byte("x")); err == nil {
					ok.Add(1)
				}
				cancel()
			}
		})
	}
	wg.Wait()
	time.Sleep(300 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tot, err := c.Total(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	// A publish that reported uncertainty may still have been queued, so queued >= successes;
	// a success without a queued message would be the bug.
	if tot.Pending < uint64(ok.Load()) {
		t.Fatalf("%d publishes reported success but only %d messages are queued", ok.Load(), tot.Pending)
	}
}

// Regression (B2): Reprocess must leave the message in the DLQ when it cannot republish it.
func TestReprocessNeverLosesAMessage(t *testing.T) {
	raw := brokerURL(t)
	p := newNetProxy(t, raw, 0)
	sfx := time.Now().UnixNano()
	main, other := fmt.Sprintf("nf_rp_main_%d", sfx), fmt.Sprintf("nf_rp_other_%d", sfx)
	c := clientVia(t, p.url(raw), nil, Queue(main).DeadLetter(), Queue(other))
	co, _ := amqp.Dial(raw)
	defer co.Close()
	ch, _ := co.Channel()
	if err := ch.PublishWithContext(context.Background(), "", main+"_dlq", false, false, amqp.Publishing{MessageId: "job", Body: []byte("IMPORTANT")}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if _, err := ch.QueueDelete(main, false, false, false); err != nil {
		t.Fatal(err)
	}
	p.delay.Store(int64(250 * time.Millisecond))
	ctxA, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	_, _ = c.Publish(ctxA, other, []byte("A"))
	cancel()
	res, err := c.Reprocess(context.Background(), main, 1)
	if err != nil {
		t.Fatal(err)
	}
	if res.Reprocessed != 0 || res.Failed != 1 {
		t.Fatalf("Reprocess = %+v, want reprocessed=0 failed=1 (the main queue does not exist)", res)
	}
	time.Sleep(500 * time.Millisecond)
	ch2, _ := co.Channel()
	st, err := ch2.QueueDeclarePassive(main+"_dlq", true, false, false, false, nil)
	if err != nil || st.Messages != 1 {
		t.Fatalf("DLQ after Reprocess: %+v %v, want the message still there", st, err)
	}
}

// Regression (G1): publishers pipeline instead of serializing on 1/RTT, and
// Status stays responsive while they run.
func TestPublishersPipelineAndStatusStaysResponsive(t *testing.T) {
	raw := brokerURL(t)
	p := newNetProxy(t, raw, 4*time.Millisecond)
	q := fmt.Sprintf("nf_pipe_%d", time.Now().UnixNano())
	c := clientVia(t, p.url(raw), nil, Queue(q))
	ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	var ok atomic.Int64
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			for ctx.Err() == nil {
				if _, err := c.Publish(context.Background(), q, []byte("x")); err == nil {
					ok.Add(1)
				}
			}
		})
	}
	var lats []time.Duration
	for ctx.Err() == nil {
		s := time.Now()
		_ = c.Status()
		lats = append(lats, time.Since(s))
		time.Sleep(20 * time.Millisecond)
	}
	wg.Wait()
	sort.Slice(lats, func(i, j int) bool { return lats[i] < lats[j] })
	rate := float64(ok.Load()) / 3
	// serialized, 4 ms of added RTT would cap this near 250 msg/s
	if rate < 2000 {
		t.Errorf("%0.f confirmed msg/s with 32 publishers and 4 ms RTT: publishers are serialized", rate)
	}
	if worst := lats[len(lats)-1]; worst > 100*time.Millisecond {
		t.Errorf("Status() took %v while publishers ran", worst)
	}
	t.Logf("pipelined: %.0f msg/s, Status max %v", rate, lats[len(lats)-1])
}

// Regression (G3): max_attempts counts handler invocations.
func TestMaxAttemptsCountsHandlerInvocations(t *testing.T) {
	raw := brokerURL(t)
	name := fmt.Sprintf("nf_attempts_%d", time.Now().UnixNano())
	c := integrationClient(t, Queue(name).DeadLetter().WithRetry(50*time.Millisecond, 3))
	_ = raw
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c.started.Store(false)
	c.lifeCtx = ctx
	var calls atomic.Int64
	if err := c.AddConsumer(name, func(context.Context, *Delivery) error { calls.Add(1); return errors.New("always fails") }); err != nil {
		t.Fatal(err)
	}
	if err := c.startConsumers(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Publish(ctx, name, []byte("x")); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(6 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if tot, err := c.Total(ctx, name); err == nil && tot.Failed == 1 {
			break
		}
	}
	time.Sleep(300 * time.Millisecond)
	_ = c.stopConsumers(context.Background())
	if calls.Load() != 3 {
		t.Fatalf("handler ran %d times with max_attempts=3, want 3", calls.Load())
	}
}

// Chaos: at-least-once through repeated connection cuts, with the consumers
// restarted by the supervisor each time.
func TestAtLeastOnceThroughConnectionCuts(t *testing.T) {
	if testing.Short() {
		t.Skip("chaos test")
	}
	raw := brokerURL(t)
	p := newNetProxy(t, raw, 0)
	q := fmt.Sprintf("nf_chaos_%d", time.Now().UnixNano())
	c := clientVia(t, p.url(raw), nil, Queue(q))
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	c.started.Store(false)
	c.lifeCtx = ctx
	var mu sync.Mutex
	seen := map[string]int{}
	if err := c.AddConsumer(q, func(_ context.Context, d *Delivery) error { mu.Lock(); seen[string(d.Body)]++; mu.Unlock(); return nil }, WithConcurrency(4)); err != nil {
		t.Fatal(err)
	}
	if err := c.startConsumers(ctx, nil); err != nil {
		t.Fatal(err)
	}
	go c.supervise(ctx, func(error) {}, nil)
	const n = 4000
	go func() {
		for i := 0; i < 6 && ctx.Err() == nil; i++ {
			time.Sleep(400 * time.Millisecond)
			p.cut()
			time.Sleep(100 * time.Millisecond)
			p.restore()
		}
	}()
	for i := range n {
		id := fmt.Sprintf("m%d", i)
		for ctx.Err() == nil {
			if _, err := c.Publish(ctx, q, []byte(id), WithMessageID(id)); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		got := len(seen)
		mu.Unlock()
		if got == n {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	missing := 0
	for i := range n {
		if seen[fmt.Sprintf("m%d", i)] == 0 {
			missing++
		}
	}
	if missing != 0 {
		t.Fatalf("%d of %d messages never reached the consumer", missing, n)
	}
}

// Trace context crosses the broker: the consumer's span continues the producer's trace.
func TestTraceContinuesAcrossTheBroker(t *testing.T) {
	name := fmt.Sprintf("nf_trace_%d", time.Now().UnixNano())
	c := integrationClient(t, Queue(name))
	c.tracer = sdktraceProvider(t).Tracer("test")
	c.trustIncoming = true
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c.started.Store(false)
	c.lifeCtx = ctx
	got := make(chan string, 1)
	if err := c.AddConsumer(name, func(hctx context.Context, d *Delivery) error {
		got <- traceIDOf(hctx)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.startConsumers(ctx, nil); err != nil {
		t.Fatal(err)
	}
	pubCtx, span := c.tracer.Start(ctx, "request")
	want := traceIDOf(pubCtx)
	if _, err := c.Publish(pubCtx, name, []byte("x")); err != nil {
		t.Fatal(err)
	}
	span.End()
	select {
	case id := <-got:
		if id != want {
			t.Fatalf("consumer trace %s, want %s", id, want)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	_ = c.stopConsumers(context.Background())
}
