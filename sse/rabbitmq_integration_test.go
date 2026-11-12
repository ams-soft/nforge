//go:build integration

package sse_test

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ams-soft/nforge/app"
	"github.com/ams-soft/nforge/codec"
	nhttp "github.com/ams-soft/nforge/http"
	"github.com/ams-soft/nforge/internal/apptest"
	"github.com/ams-soft/nforge/logs"
	"github.com/ams-soft/nforge/message"
	"github.com/ams-soft/nforge/module"
	"github.com/ams-soft/nforge/sse"
	amqp "github.com/rabbitmq/amqp091-go"
)

// brokerURL skips the test unless a disposable RabbitMQ is configured, as the
// message integration tests do (NFORGE_TEST_RABBITMQ_URL).
func brokerURL(t testing.TB) string {
	t.Helper()
	u := os.Getenv("NFORGE_TEST_RABBITMQ_URL")
	if u == "" {
		t.Skip("NFORGE_TEST_RABBITMQ_URL is not set")
	}
	return u
}

// proxy forwards TCP to the broker and can cut and restore the path, which is
// how the tests produce a broker outage without touching the broker.
type proxy struct {
	t      testing.TB
	target string
	addr   string
	mu     sync.Mutex
	ln     net.Listener
	conns  []net.Conn
}

func newProxy(t testing.TB, brokerURL string) *proxy {
	t.Helper()
	u, err := url.Parse(brokerURL)
	if err != nil {
		t.Fatal(err)
	}
	host := u.Host
	if u.Port() == "" {
		host += ":5672"
	}
	p := &proxy{t: t, target: host, addr: fmt.Sprintf("127.0.0.1:%d", freePort(t))}
	p.restore()
	t.Cleanup(p.cut)
	return p
}

func (p *proxy) url(brokerURL string) string {
	u, _ := url.Parse(brokerURL)
	u.Host = p.addr
	return u.String()
}

func (p *proxy) restore() {
	var ln net.Listener
	var err error
	for range 50 {
		if ln, err = net.Listen("tcp", p.addr); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		p.t.Fatal(err)
	}
	p.mu.Lock()
	p.ln = ln
	p.mu.Unlock()
	go func() {
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
			go func() { _, _ = io.Copy(up, c); _ = up.Close(); _ = c.Close() }()
			go func() { _, _ = io.Copy(c, up); _ = up.Close(); _ = c.Close() }()
		}
	}()
}

func (p *proxy) cut() {
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

func randomNamespace() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return "ssetest" + hex.EncodeToString(b)
}

// userModule opens the stream with a test-only identity: the X-Test-User header.
func userModule() module.Descriptor {
	return module.New("streams", module.Definition{
		Requires: []module.Handle{nhttp.Capability, sse.Capability},
		Init: func(_ context.Context, sc *module.Scope) (module.Hooks, error) {
			server, mgr := module.Require(sc, nhttp.Capability), module.Require(sc, sse.Capability)
			server.Router().Get("/stream", mgr.Handler(sse.WithIdentityResolver(func(r *http.Request) (string, error) {
				if u := r.Header.Get("X-Test-User"); u != "" {
					return u, nil
				}
				return "", sse.ErrUnauthorized
			})).ServeHTTP)
			return module.Hooks{}, nil
		},
	})
}

type instance struct {
	app  *app.Application
	mgr  *sse.Manager
	base string
}

func startInstance(t *testing.T, _, ns, brokerURL string) *instance {
	t.Helper()
	port := freePort(t)
	yaml := fmt.Sprintf(`http:
  server: {host: 127.0.0.1, port: %d}
message:
  enabled: true
  namespace: %s
  rabbitmq:
    url: %s
    reconnect: {initial_delay: 100ms, max_delay: 400ms, multiplier: 2, jitter: false}
    dial_timeout: 2s
  publisher: {confirm_timeout: 3s}
sse:
  driver: rabbitmq
  heartbeat: 1s
`, port, ns, brokerURL)
	d := apptest.Load(t, yaml, "")
	a, err := app.New(d, app.Modules(logs.Module(), codec.Module(), message.Module(), nhttp.Module(), sse.Module(), userModule()), apptest.Quiet(), app.WithProbeInterval(time.Nanosecond, time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = a.Shutdown(ctx)
	})
	return &instance{app: a, mgr: value(a, sse.Capability), base: fmt.Sprintf("http://127.0.0.1:%d", port)}
}

type stream struct {
	lines chan string
	resp  *http.Response
}

func (i *instance) open(t *testing.T, user string) *stream {
	t.Helper()
	req, _ := http.NewRequestWithContext(t.Context(), "GET", i.base+"/stream", nil)
	req.Header.Set("X-Test-User", user)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("stream status %d", resp.StatusCode)
	}
	s := &stream{lines: make(chan string, 256), resp: resp}
	go func() {
		defer close(s.lines)
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			s.lines <- sc.Text()
		}
	}()
	t.Cleanup(func() { resp.Body.Close() })
	return s
}

// next returns the next line that satisfies want, or false after the timeout.
func (s *stream) next(timeout time.Duration, want func(string) bool) (string, bool) {
	deadline := time.After(timeout)
	for {
		select {
		case l, ok := <-s.lines:
			if !ok {
				return "", false
			}
			if want(l) {
				return l, true
			}
		case <-deadline:
			return "", false
		}
	}
}

func data(l string) bool      { return strings.HasPrefix(l, "data:") }
func heartbeat(l string) bool { return l == ": heartbeat" }

func eventually(t testing.TB, timeout time.Duration, what string, f func() bool) {
	t.Helper()
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); time.Sleep(25 * time.Millisecond) {
		if f() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func ready(a *app.Application) bool {
	return a.Status(context.Background()).Ready
}

func TestTwoInstancesBothReceiveAndUsersAreIsolated(t *testing.T) {
	broker := brokerURL(t)
	ns := randomNamespace()
	a, b := startInstance(t, "inst-a", ns, broker), startInstance(t, "inst-b", ns, broker)

	alice1, alice2, aliceB := a.open(t, "alice"), a.open(t, "alice"), b.open(t, "alice")
	bobB := b.open(t, "bob")
	eventually(t, 5*time.Second, "streams", func() bool {
		return a.mgr.Stats().ActiveConnections == 2 && b.mgr.Stats().ActiveConnections == 2
	})

	res, err := a.mgr.PublishToUser(t.Context(), "alice", sse.Event{ID: "e1", Name: "note", Data: map[string]string{"k": "v"}})
	if err != nil || !res.Accepted {
		t.Fatalf("publish: %+v %v", res, err)
	}
	for name, s := range map[string]*stream{"alice tab 1 on A": alice1, "alice tab 2 on A": alice2, "alice on B": aliceB} {
		if l, ok := s.next(5*time.Second, data); !ok || l != `data: {"k":"v"}` {
			t.Fatalf("%s: %q %v", name, l, ok)
		}
	}
	// publishing from the other instance reaches the first one too
	if _, err := b.mgr.PublishToUser(t.Context(), "alice", sse.Event{Data: "from B"}); err != nil {
		t.Fatal(err)
	}
	for name, s := range map[string]*stream{"A1": alice1, "A2": alice2, "B": aliceB} {
		if l, ok := s.next(5*time.Second, data); !ok || l != "data: from B" {
			t.Fatalf("%s: %q %v", name, l, ok)
		}
	}
	if l, ok := bobB.next(500*time.Millisecond, data); ok {
		t.Fatalf("bob received %q", l)
	}
	// every instance saw the message, but only filtered locally: nothing leaked, nothing malformed
	if a.mgr.Stats().MalformedMessages != 0 || b.mgr.Stats().MalformedMessages != 0 {
		t.Error("unexpected malformed messages")
	}
}

func TestNoOfflineReplayAndQueuesAreAutoDeleted(t *testing.T) {
	broker := brokerURL(t)
	ns := randomNamespace()
	a := startInstance(t, "inst-a", ns, broker)

	res, err := a.mgr.PublishToUser(t.Context(), "carol", sse.Event{Data: "while offline"})
	if err != nil || !res.Accepted {
		t.Fatalf("a healthy transport with nobody connected is a successful no-op: %+v %v", res, err)
	}
	c := a.open(t, "carol")
	if l, ok := c.next(700*time.Millisecond, data); ok {
		t.Fatalf("replayed %q", l)
	}

	// after shutdown no queue stays bound to the exchange: a mandatory publish is unroutable
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := a.app.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	conn, err := amqp.Dial(broker)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ch, _ := conn.Channel()
	exchange := ""
	// the exchange name is derived from namespace, env, app name: find it by trying the documented shape
	exchange = ns + ".sse.local.test-app"
	if err := ch.ExchangeDeclarePassive(exchange, "fanout", true, false, false, false, nil); err != nil {
		t.Fatalf("the fanout exchange %q should exist (durable): %v", exchange, err)
	}
	ch2, _ := conn.Channel()
	returns := ch2.NotifyReturn(make(chan amqp.Return, 1))
	_ = ch2.Confirm(false)
	if err := ch2.PublishWithContext(ctx, exchange, "", true, false, amqp.Publishing{Body: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-returns:
	case <-time.After(3 * time.Second):
		t.Fatal("a queue is still bound to the exchange after the instance stopped")
	}
	_ = ch.ExchangeDelete(exchange, false, false)
}

func TestBrokerOutageKeepsStreamsAndRestoresDelivery(t *testing.T) {
	broker := brokerURL(t)
	p := newProxy(t, broker)
	ns := randomNamespace()
	a, b := startInstance(t, "inst-a", ns, p.url(broker)), startInstance(t, "inst-b", ns, p.url(broker))
	sa, sb := a.open(t, "alice"), b.open(t, "alice")
	eventually(t, 5*time.Second, "streams", func() bool { return a.mgr.Stats().ActiveConnections == 1 && b.mgr.Stats().ActiveConnections == 1 })
	if _, err := a.mgr.PublishToUser(t.Context(), "alice", sse.Event{Data: "before"}); err != nil {
		t.Fatal(err)
	}
	for _, s := range []*stream{sa, sb} {
		if _, ok := s.next(5*time.Second, data); !ok {
			t.Fatal("no delivery before the outage")
		}
	}

	p.cut() // the broker becomes unreachable

	eventually(t, 10*time.Second, "publish to report the outage", func() bool {
		_, err := a.mgr.PublishToUser(t.Context(), "alice", sse.Event{Data: "during"})
		return errors.Is(err, sse.ErrTransportUnavailable) || errors.Is(err, sse.ErrPublishUncertain)
	})
	if res, err := a.mgr.PublishToUser(t.Context(), "alice", sse.Event{Data: "during"}); err == nil || res.Accepted {
		t.Fatalf("a publication during the outage must not report acceptance: %+v %v", res, err)
	}
	eventually(t, 10*time.Second, "module not ready", func() bool { return !ready(a.app) })

	// the HTTP connections stay open and heartbeats continue
	for name, s := range map[string]*stream{"A": sa, "B": sb} {
		if _, ok := s.next(4*time.Second, heartbeat); !ok {
			t.Fatalf("stream %s lost its heartbeat during the outage", name)
		}
	}
	if a.mgr.Stats().ActiveConnections != 1 || b.mgr.Stats().ActiveConnections != 1 {
		t.Fatal("the outage closed HTTP streams")
	}

	p.restore()

	// Publications confirmed before the instances re-bound their queues are lost
	// by design (best effort), so wait for readiness, then publish until seen.
	eventually(t, 30*time.Second, "both instances ready again", func() bool { return ready(a.app) && ready(b.app) })
	for name, s := range map[string]*stream{"A": sa, "B": sb} {
		if l, ok := receiveAfterRetries(t, a, s, "alice", "after", 15*time.Second); !ok {
			t.Fatalf("stream %s did not receive after recovery (last %q)", name, l)
		}
	}
	eventually(t, 10*time.Second, "ready again", func() bool { return ready(a.app) && ready(b.app) })
	if a.mgr.Stats().BrokerReconnects < 1 {
		t.Errorf("reconnects = %d", a.mgr.Stats().BrokerReconnects)
	}
}

// A real broker restart loses the non-durable queues (and, on tmpfs, the
// exchange): the topology must be rebuilt. It needs a container to restart, so
// it only runs when NFORGE_TEST_RABBITMQ_CONTAINER names one.
func TestBrokerRestartRebuildsTheTopology(t *testing.T) {
	broker := brokerURL(t)
	container := os.Getenv("NFORGE_TEST_RABBITMQ_CONTAINER")
	if container == "" {
		t.Skip("NFORGE_TEST_RABBITMQ_CONTAINER is not set")
	}
	a := startInstance(t, "inst-a", randomNamespace(), broker)
	s := a.open(t, "alice")
	eventually(t, 5*time.Second, "stream", func() bool { return a.mgr.Stats().ActiveConnections == 1 })
	if out, err := exec.Command("docker", "restart", container).CombinedOutput(); err != nil {
		t.Fatalf("docker restart: %v %s", err, out)
	}
	eventually(t, 90*time.Second, "ready after the broker restart", func() bool { return ready(a.app) })
	if _, ok := receiveAfterRetries(t, a, s, "alice", "restarted", 30*time.Second); !ok {
		t.Fatal("no delivery after the broker restart")
	}
}

// receiveAfterRetries publishes payload every 200ms until the stream shows it.
func receiveAfterRetries(t *testing.T, from *instance, s *stream, user, payload string, timeout time.Duration) (string, bool) {
	t.Helper()
	want := "data: " + payload
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	deadline := time.After(timeout)
	for {
		_, _ = from.mgr.PublishToUser(t.Context(), user, sse.Event{Data: payload})
		select {
		case l, ok := <-s.lines:
			if !ok {
				return "", false
			}
			if l == want {
				return l, true
			}
		case <-tick.C:
		case <-deadline:
			return "", false
		}
	}
}
