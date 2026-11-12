package sse_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ams-soft/nforge/app"
	"github.com/ams-soft/nforge/cache"
	"github.com/ams-soft/nforge/codec"
	nhttp "github.com/ams-soft/nforge/http"
	"github.com/ams-soft/nforge/internal/apptest"
	"github.com/ams-soft/nforge/jose"
	"github.com/ams-soft/nforge/logs"
	"github.com/ams-soft/nforge/message"
	"github.com/ams-soft/nforge/metrics"
	"github.com/ams-soft/nforge/module"
	"github.com/ams-soft/nforge/sse"
)

func freePort(t testing.TB) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

const joseYAML = `
jose:
  enabled: true
  keys:
    - kid: signing
      secret: 0123456789abcdef0123456789abcdef0123456789abcdef
  profiles:
    - name: access
      kind: jwt_signed
      typ: at+jwt
      signing_key: signing
      verification_keys: [signing]
      jwt: {issuer: https://issuer.test, audiences: [sse-test], ttl: 15m, max_ttl: 15m}
`

// streamModule registers the stream route behind real bearer authentication.
func streamModule() module.Descriptor {
	return module.New("streams", module.Definition{
		Requires: []module.Handle{nhttp.Capability, sse.Capability, jose.Capability},
		Init: func(_ context.Context, sc *module.Scope) (module.Hooks, error) {
			server, mgr, j := module.Require(sc, nhttp.Capability), module.Require(sc, sse.Capability), module.Require(sc, jose.Capability)
			server.Router().With(nhttp.Authenticate(j, "access")).Get("/notifications/stream", mgr.Handler().ServeHTTP)
			return module.Hooks{}, nil
		},
	})
}

type env struct {
	app  *app.Application
	base string
	mgr  *sse.Manager
	jose *jose.Service
}

func value[T any](a *app.Application, c module.Capability[T]) T {
	v, ok := app.Value(a, c)
	if !ok {
		panic("capability not published")
	}
	return v
}

func (e *env) token(t testing.TB, subject string) string {
	t.Helper()
	tok, err := e.jose.IssueJWT(context.Background(), "access", jose.RegisteredClaims{Subject: subject}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func startEnv(t *testing.T, httpYAML, sseYAML string) *env {
	t.Helper()
	port := freePort(t)
	yaml := fmt.Sprintf("http:\n  server:\n    host: 127.0.0.1\n    port: %d\n%s\n%s\n%s", port, httpYAML, sseYAML, joseYAML)
	a := apptest.Start(t, yaml, logs.Module(), codec.Module(), jose.Module(), nhttp.Module(), sse.Module(), streamModule())
	return &env{app: a, base: fmt.Sprintf("http://127.0.0.1:%d", port), mgr: value(a, sse.Capability), jose: value(a, jose.Capability)}
}

func (e *env) open(t testing.TB, token string) (*http.Response, *bufio.Scanner) {
	t.Helper()
	req, _ := http.NewRequestWithContext(t.Context(), "GET", e.base+"/notifications/stream", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp, bufio.NewScanner(resp.Body)
}

func TestAuthenticatedStreamThroughTheRealChain(t *testing.T) {
	e := startEnv(t, "", "sse: {heartbeat: 1s}")

	if resp, _ := e.open(t, ""); resp.StatusCode != 401 {
		t.Fatalf("no token: %d, want 401", resp.StatusCode)
	}
	if resp, _ := e.open(t, "not-a-token"); resp.StatusCode != 401 {
		t.Fatalf("bad token: %d, want 401", resp.StatusCode)
	}

	resp, sc := e.open(t, e.token(t, "user-1"))
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("%d %v", resp.StatusCode, resp.Header)
	}
	other, osc := e.open(t, e.token(t, "user-2"))
	_ = other
	deadline := time.Now().Add(5 * time.Second)
	for e.mgr.Stats().ActiveConnections != 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	// the identity is the validated principal (issuer, tenant, subject): a client cannot pick another user
	if _, err := e.mgr.PublishToUser(t.Context(), sse.UserKeyOf("https://issuer.test", "", "user-1"), sse.Event{ID: "n1", Name: "note", Data: map[string]int{"n": 1}}); err != nil {
		t.Fatal(err)
	}
	got := readEvent(t, sc)
	if got != "id: n1\nevent: note\ndata: {\"n\":1}\n" {
		t.Fatalf("event = %q", got)
	}
	osc2 := make(chan string, 1)
	go func() {
		for osc.Scan() {
			if strings.HasPrefix(osc.Text(), "data:") {
				osc2 <- osc.Text()
				return
			}
		}
	}()
	select {
	case l := <-osc2:
		t.Fatalf("user-2 received %q", l)
	case <-time.After(300 * time.Millisecond):
	}
}

func readEvent(t testing.TB, sc *bufio.Scanner) string {
	t.Helper()
	var b strings.Builder
	done := make(chan struct{})
	go func() {
		defer close(done)
		for sc.Scan() {
			line := sc.Text()
			if line == "" {
				if strings.Contains(b.String(), "data:") {
					return
				}
				b.Reset()
				continue
			}
			if strings.HasPrefix(line, ":") || strings.HasPrefix(line, "retry:") {
				continue
			}
			b.WriteString(line + "\n")
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out reading an event")
	}
	return b.String()
}

// With every request timeout short and enabled, a stream still lives on and
// keeps its heartbeat: SSE is exempt from them.
func TestStreamIsExemptFromShortServerTimeouts(t *testing.T) {
	e := startEnv(t, `    read_header_timeout: 300ms
    read_timeout: 600ms
    write_timeout: 600ms
    handler_timeout: 600ms
`, "sse: {heartbeat: 1s}")
	resp, sc := e.open(t, e.token(t, "u"))
	if resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	beats := 0
	deadline := time.AfterFunc(3500*time.Millisecond, func() { resp.Body.Close() })
	defer deadline.Stop()
	for sc.Scan() {
		if sc.Text() == ": heartbeat" {
			beats++
		}
	}
	if beats < 3 {
		t.Fatalf("only %d heartbeats before the stream ended: %v", beats, sc.Err())
	}
}

// The same with the default (30 s) server timeouts, past the 30 s mark.
func TestStreamSurvivesMoreThan30Seconds(t *testing.T) {
	if testing.Short() {
		t.Skip("streams for 33 s")
	}
	e := startEnv(t, "", "sse: {heartbeat: 5s}")
	resp, sc := e.open(t, e.token(t, "u"))
	if resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	beats := 0
	timer := time.AfterFunc(33*time.Second, func() { resp.Body.Close() })
	defer timer.Stop()
	start := time.Now()
	for sc.Scan() {
		if sc.Text() == ": heartbeat" {
			beats++
		}
	}
	if beats < 6 || time.Since(start) < 32*time.Second {
		t.Fatalf("stream ended after %s with %d heartbeats: %v", time.Since(start), beats, sc.Err())
	}
	// still publishing works after the 30 s mark on a fresh stream
	if _, err := e.mgr.PublishToUser(t.Context(), "u", sse.Event{Data: "late"}); err != nil {
		t.Fatal(err)
	}
}

// An ordinary route keeps the opt-in timeout while the stream does not.
func TestOptInTimeoutStillAppliesToOrdinaryRoutes(t *testing.T) {
	port := freePort(t)
	yaml := fmt.Sprintf("http:\n  server:\n    host: 127.0.0.1\n    port: %d\n    handler_timeout: 200ms\n", port)
	var sawDeadline bool
	routes := module.New("routes", module.Definition{Requires: []module.Handle{nhttp.Capability}, Init: func(_ context.Context, sc *module.Scope) (module.Hooks, error) {
		module.Require(sc, nhttp.Capability).Router().Get("/ctx", func(w http.ResponseWriter, r *http.Request) {
			_, sawDeadline = r.Context().Deadline()
		})
		return module.Hooks{}, nil
	}})
	apptest.Start(t, yaml, logs.Module(), codec.Module(), nhttp.Module(), routes)
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/ctx", port))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if !sawDeadline {
		t.Fatal("a positive handler_timeout must apply to ordinary routes")
	}
}

func TestGracefulShutdownWithOpenStreams(t *testing.T) {
	port := freePort(t)
	yaml := fmt.Sprintf("http:\n  server:\n    host: 127.0.0.1\n    port: %d\nsse: {heartbeat: 1s}\n%s", port, joseYAML)
	a, err := apptest.New(t, yaml, logs.Module(), codec.Module(), jose.Module(), nhttp.Module(), sse.Module(), streamModule())
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	e := &env{app: a, base: fmt.Sprintf("http://127.0.0.1:%d", port), mgr: value(a, sse.Capability), jose: value(a, jose.Capability)}
	var bodies []io.ReadCloser
	for i := range 3 {
		resp, _ := e.open(t, e.token(t, fmt.Sprintf("u%d", i)))
		bodies = append(bodies, resp.Body)
	}
	deadline := time.Now().Add(5 * time.Second)
	for e.mgr.Stats().ActiveConnections != 3 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	start := time.Now()
	if err := a.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("shutdown took %s: HTTP waited for the open streams", took)
	}
	for i, b := range bodies {
		done := make(chan struct{})
		go func() { _, _ = io.Copy(io.Discard, b); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("stream %d still open after shutdown", i)
		}
	}
	if _, err := e.mgr.PublishToUser(t.Context(), "u0", sse.Event{}); !errors.Is(err, sse.ErrClosed) {
		t.Fatalf("publish after shutdown: %v", err)
	}
}

func TestModuleGraphRules(t *testing.T) {
	base := []module.Descriptor{logs.Module(), codec.Module(), nhttp.Module()}
	build := func(yaml string, mods ...module.Descriptor) error {
		_, err := apptest.New(t, yaml, append(append([]module.Descriptor{}, base...), mods...)...)
		return err
	}
	if err := build("", sse.Module()); err != nil {
		t.Fatalf("registered with no section must use the defaults: %v", err)
	}
	if err := build("sse: {driver: memory}\n", sse.Module()); err != nil {
		t.Fatalf("memory needs no Messaging: %v", err)
	}
	if err := build("sse: {driver: rabbitmq}\n", sse.Module()); !errors.Is(err, module.ErrMissingDependency) {
		t.Fatalf("rabbitmq without the message module: %v", err)
	}
	if err := build("sse: {driver: rabbitmq}\nmessage: {enabled: false}\n", sse.Module(), message.Module()); !errors.Is(err, module.ErrDisabledDependency) {
		t.Fatalf("rabbitmq with Messaging disabled: %v", err)
	}
	if err := build("sse: {enabled: true}\n"); err == nil {
		t.Fatal("an sse section without the registered module must fail like every module")
	}
	if err := build("sse: {driver: kafka}\n", sse.Module()); err == nil {
		t.Fatal("an unknown driver must fail at app.New")
	}
	if err := build("sse: {bogus: 1}\n", sse.Module()); err == nil {
		t.Fatal("an unknown key must fail at app.New")
	}
	if err := build("sse: {enabled: false}\n", sse.Module()); err != nil {
		t.Fatalf("disabled: %v", err)
	}
	if err := build("", sse.Module()); err != nil {
		t.Fatal(err)
	}
	// a module with no codec json fails the cross-section check
	if _, err := apptest.New(t, "codec: {formats: {enabled: [cbor]}, default: cbor}\n", logs.Module(), codec.Module(), nhttp.Module(), sse.Module()); err == nil {
		t.Fatal("sse needs json in codec.formats.enabled")
	}
}

// G20: the metrics module collects the framework's own instruments and nhttp serves them.
func TestMetricsEndpointExposesTheFrameworkInstruments(t *testing.T) {
	port := freePort(t)
	yaml := fmt.Sprintf("http:\n  server:\n    host: 127.0.0.1\n    port: %d\n  routes:\n    metrics: {enabled: true}\ncache: {enabled: true, driver: memory}\nsse: {heartbeat: 1s}\n%s", port, joseYAML)
	a := apptest.Start(t, yaml, logs.Module(), codec.Module(), jose.Module(), metrics.Module(), cache.Module(), nhttp.Module(), sse.Module(), streamModule())
	e := &env{app: a, base: fmt.Sprintf("http://127.0.0.1:%d", port), mgr: value(a, sse.Capability), jose: value(a, jose.Capability)}

	resp, _ := e.open(t, e.token(t, "u1"))
	if resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	deadline := time.Now().Add(5 * time.Second)
	for e.mgr.Stats().ActiveConnections != 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := e.mgr.PublishToUser(t.Context(), "nobody", sse.Event{Data: "x"}); err != nil {
		t.Fatal(err)
	}
	c := value(a, cache.Capability)
	_ = c.Set(t.Context(), "k", "v", 0)
	var got string
	_ = c.Get(t.Context(), "k", &got)
	_ = c.Get(t.Context(), "missing", &got)

	warm, err := http.Get(e.base + "/metrics") // a request is counted when it completes
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, warm.Body)
	warm.Body.Close()
	r, err := http.Get(e.base + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	out := string(b)
	for _, want := range []string{
		`nforge_http_requests_total{`,
		`nforge_http_request_duration_seconds_bucket{`,
		"nforge_sse_connections 1\n",
		"nforge_sse_events_published_total 1\n",
		"nforge_sse_connections_accepted_total 1\n",
		`nforge_cache_hits_total{driver="memory"} 1`,
		`nforge_cache_misses_total{driver="memory"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in /metrics", want)
		}
	}
	if strings.Contains(out, "u1") || strings.Contains(out, "nobody") {
		t.Error("user IDs must never appear in the metrics")
	}
}

func TestMetricsRouteRequiresTheMetricsModule(t *testing.T) {
	yaml := "http:\n  server: {host: 127.0.0.1, port: 8080}\n  routes: {metrics: {enabled: true}}\n"
	if _, err := apptest.New(t, yaml, logs.Module(), codec.Module(), nhttp.Module()); err == nil || !strings.Contains(err.Error(), "metrics module") {
		t.Fatalf("err = %v, want a clear message about the metrics module", err)
	}
	if _, err := apptest.New(t, yaml, logs.Module(), codec.Module(), metrics.Module(), nhttp.Module()); err != nil {
		t.Fatalf("with the module registered: %v", err)
	}
}
