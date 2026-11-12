package sse

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ams-soft/nforge/codec"
)

func testConfig(mutate func(*Config)) Config {
	var c Config
	c.ApplyDefaults()
	if mutate != nil {
		mutate(&c)
	}
	return c
}

func testRegistry(t testing.TB) *codec.Registry {
	t.Helper()
	r, err := codec.New(codec.Config{Formats: []string{"json"}, Default: "json", MaxPayloadBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// headerIdentity trusts X-Test-User. Only a test may do this.
func headerIdentity(r *http.Request) (string, error) {
	if u := r.Header.Get("X-Test-User"); u != "" {
		return u, nil
	}
	return "", ErrUnauthorized
}

// startManager builds a started memory-driver manager.
func startManager(t testing.TB, mutate func(*Config)) *Manager {
	t.Helper()
	m := newManager(testConfig(mutate), testRegistry(t), nil, &memoryDriver{})
	if err := m.start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = m.close(ctx)
	})
	return m
}

func serverFor(t testing.TB, m *Manager, opts ...HandlerOption) *httptest.Server {
	t.Helper()
	opts = append([]HandlerOption{WithIdentityResolver(headerIdentity)}, opts...)
	srv := httptest.NewServer(m.Handler(opts...))
	t.Cleanup(srv.Close)
	return srv
}

// client is one open stream read line by line.
type client struct {
	resp *http.Response
	rd   *bufio.Reader
	t    testing.TB
}

func openStream(t testing.TB, url, user string, hdr ...string) *client {
	t.Helper()
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if user != "" {
		req.Header.Set("X-Test-User", user)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return &client{resp: resp, rd: bufio.NewReader(resp.Body), t: t}
}

// frame reads lines up to the blank line that ends a frame.
func (c *client) frame(timeout time.Duration) (string, bool) {
	c.t.Helper()
	type res struct {
		s   string
		err error
	}
	ch := make(chan res, 1)
	go func() {
		var b strings.Builder
		for {
			line, err := c.rd.ReadString('\n')
			if err != nil {
				ch <- res{b.String(), err}
				return
			}
			b.WriteString(line)
			if line == "\n" {
				ch <- res{b.String(), nil}
				return
			}
		}
	}()
	select {
	case r := <-ch:
		return r.s, r.err == nil
	case <-time.After(timeout):
		return "", false
	}
}

// event skips comments and the retry preamble and returns the next event frame.
func (c *client) event(timeout time.Duration) (string, bool) {
	c.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		f, ok := c.frame(time.Until(deadline))
		if !ok {
			return f, false
		}
		if strings.Contains(f, "data:") {
			return f, true
		}
	}
	return "", false
}

func (c *client) ended(timeout time.Duration) bool {
	c.t.Helper()
	done := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, c.resp.Body); close(done) }()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

func waitFor(t testing.TB, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func newReader(resp *http.Response) *bufio.Reader { return bufio.NewReader(resp.Body) }
