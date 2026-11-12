package main

import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ams-soft/nforge/app"
	"github.com/ams-soft/nforge/config"
	"github.com/ams-soft/nforge/sse"
)

// start loads the real config.yaml (memory driver) on a free port.
func start(t *testing.T) (string, *sse.Manager) {
	t.Helper()
	t.Setenv("JWT_SECRET", "0123456789abcdef0123456789abcdef0123456789abcdef")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	env := filepath.Join(t.TempDir(), "test.env")
	text := fmt.Sprintf("LOGGING_OUTPUT_TERMINAL_ENABLED=false\nHTTP_SERVER_HEADER=false\nHTTP_SERVER_PORT=%d\n", port)
	if err := os.WriteFile(env, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFlags("config.yaml", env)
	if err != nil {
		t.Fatal(err)
	}
	a, err := newApp(cfg, app.WithLogger(slog.New(slog.DiscardHandler)))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Shutdown(t.Context()) })
	m, _ := app.Value(a, sse.Capability)
	return fmt.Sprintf("http://127.0.0.1:%d", port), m
}

// browser is a client with its own cookie jar, like a browser profile.
func browser(t *testing.T, base, user string) *http.Client {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	resp, err := c.Post(base+"/login", "application/json", strings.NewReader(`{"user":"`+user+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("login %d", resp.StatusCode)
	}
	return c
}

func openStream(t *testing.T, c *http.Client, base string) (*http.Response, chan string) {
	t.Helper()
	req, _ := http.NewRequestWithContext(t.Context(), "GET", base+"/notifications/stream", nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	lines := make(chan string, 64)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	return resp, lines
}

func waitData(lines chan string, d time.Duration) (string, bool) {
	timeout := time.After(d)
	for {
		select {
		case l, ok := <-lines:
			if !ok {
				return "", false
			}
			if strings.HasPrefix(l, "data:") {
				return l, true
			}
		case <-timeout:
			return "", false
		}
	}
}

func TestTwoTabsOfOneUserReceiveAndAnotherUserDoesNot(t *testing.T) {
	base, m := start(t)
	alice, bob := browser(t, base, "alice"), browser(t, base, "bob")
	r1, tab1 := openStream(t, alice, base)
	_, tab2 := openStream(t, alice, base)
	_, bobTab := openStream(t, bob, base)
	if r1.StatusCode != 200 || !strings.HasPrefix(r1.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("%d %v", r1.StatusCode, r1.Header)
	}
	for deadline := time.Now().Add(5 * time.Second); m.Stats().ActiveConnections != 3 && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}

	resp, err := bob.Post(base+"/notify", "application/json", strings.NewReader(`{"user":"alice","message":"hi alice"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("notify %d", resp.StatusCode)
	}
	for i, tab := range []chan string{tab1, tab2} {
		if l, ok := waitData(tab, 3*time.Second); !ok || l != `data: {"message":"hi alice"}` {
			t.Fatalf("alice tab %d: %q %v", i+1, l, ok)
		}
	}
	if l, ok := waitData(bobTab, 300*time.Millisecond); ok {
		t.Fatalf("bob received alice's notification: %q", l)
	}
}

func TestStreamNeedsAValidSession(t *testing.T) {
	base, _ := start(t)
	for name, c := range map[string]*http.Client{"no cookie": {}} {
		resp, err := c.Get(base + "/notifications/stream")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 401 {
			t.Errorf("%s: %d, want 401", name, resp.StatusCode)
		}
	}
	// a client-chosen identity is ignored: the query string selects nothing
	resp, err := http.Get(base + "/notifications/stream?user_id=alice")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Errorf("user_id in the query: %d, want 401", resp.StatusCode)
	}
	// a cross-origin page cannot open the stream even with the cookie
	c := browser(t, base, "alice")
	req, _ := http.NewRequest("GET", base+"/notifications/stream", nil)
	req.Header.Set("Origin", "https://evil.example")
	r, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != 403 {
		t.Errorf("cross-origin: %d, want 403", r.StatusCode)
	}
}

func TestOrdinaryRouteOptsInToATimeout(t *testing.T) {
	base, _ := start(t)
	resp, err := http.Get(base + "/slow")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("/slow = %d %s, want 504", resp.StatusCode, b)
	}
}

func TestIndexPageServes(t *testing.T) {
	base, _ := start(t)
	resp, err := http.Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(b), "EventSource") {
		t.Fatalf("%d", resp.StatusCode)
	}
}

// The RabbitMQ variant resolves its graph (sse needs message) without a broker:
// app.New does no I/O.
func TestRabbitMQConfigurationBuilds(t *testing.T) {
	t.Setenv("JWT_SECRET", "0123456789abcdef0123456789abcdef0123456789abcdef")
	cfg, err := config.LoadFlags("config.rabbitmq.yaml", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newApp(cfg, app.WithLogger(slog.New(slog.DiscardHandler))); err != nil {
		t.Fatal(err)
	}
}
