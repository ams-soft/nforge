package sse

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	nhttp "github.com/ams-soft/nforge/http"
)

// G6: tenants or issuers sharing a subject do not share a stream.
func TestUserKeyKeepsIssuersAndTenantsApart(t *testing.T) {
	a := UserKey(nhttp.Principal{Issuer: "https://a.test", TenantID: "t1", Subject: "42"})
	for name, p := range map[string]nhttp.Principal{
		"other issuer": {Issuer: "https://b.test", TenantID: "t1", Subject: "42"},
		"other tenant": {Issuer: "https://a.test", TenantID: "t2", Subject: "42"},
		"no tenant":    {Issuer: "https://a.test", Subject: "42"},
	} {
		if UserKey(p) == a {
			t.Errorf("%s shares the key %q", name, a)
		}
	}
	// no ambiguity through the delimiters
	if UserKeyOf("a|1:b", "", "c") == UserKeyOf("a", "1:b|", "c") {
		t.Error("length prefixes must make the key unambiguous")
	}
	if UserKey(nhttp.Principal{Issuer: "i", TenantID: "t", Subject: "s"}) != UserKeyOf("i", "t", "s") {
		t.Error("UserKey and UserKeyOf must agree")
	}
	if !validText(a) {
		t.Errorf("the key must be a valid user id: %q", a)
	}
}

// G7: the stream ends at the token's expiry, after a final reauth event.
func TestStreamEndsAtTokenExpiryWithAReauthEvent(t *testing.T) {
	m := startManager(t, nil)
	srv := httptest.NewServer(m.Handler(
		WithIdentityResolver(headerIdentity),
		WithExpiry(func(*http.Request) time.Time { return time.Now().Add(400 * time.Millisecond) }),
	))
	defer srv.Close()
	c := openStream(t, srv.URL, "u")
	start := time.Now()
	var got string
	deadline := time.After(5 * time.Second)
loop:
	for {
		select {
		case <-deadline:
			t.Fatal("the stream outlived the token")
		default:
		}
		f, ok := c.frame(2 * time.Second)
		if strings.Contains(f, "event: reauth") {
			got = f
			break loop
		}
		if !ok {
			break
		}
	}
	if !strings.Contains(got, "data: expired") {
		t.Fatalf("final frame %q, want a reauth event with reason expired", got)
	}
	if d := time.Since(start); d < 300*time.Millisecond || d > 2*time.Second {
		t.Errorf("ended after %v, want about 400ms", d)
	}
	if !c.ended(2 * time.Second) {
		t.Error("the stream must close after the reauth event")
	}
}

func TestMaxLifetimeAlsoSendsReauth(t *testing.T) {
	m := startManager(t, func(c *Config) { c.MaxLifetime = 80 * time.Millisecond })
	c := openStream(t, serverFor(t, m).URL, "u")
	f, _ := c.event(0)
	_ = f
	var seen bool
	for range 20 {
		fr, ok := c.frame(2 * time.Second)
		if strings.Contains(fr, "event: reauth") && strings.Contains(fr, "data: lifetime") {
			seen = true
			break
		}
		if !ok {
			break
		}
	}
	if !seen {
		t.Fatal("max_lifetime must end with a reauth event carrying the reason")
	}
}

// G5: with a small send buffer, a client that never reads is cut once the
// buffers fill; without it the kernel would swallow far more.
func TestNeverReadingClientIsCutWithASmallSendBuffer(t *testing.T) {
	m := startManager(t, func(c *Config) {
		c.SendBufferBytes = 4096
		c.WriteTimeout = time.Second
		c.Heartbeat = 100 * time.Millisecond
		c.BufferSize = 8
	})
	srv := httptest.NewUnstartedServer(m.Handler(WithIdentityResolver(headerIdentity)))
	srv.Config.ConnContext = ConnContext(nil)
	srv.Start()
	defer srv.Close()

	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.(*net.TCPConn).SetReadBuffer(2048)
	fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: x\r\nX-Test-User: stuck\r\n\r\n")
	br := bufio.NewReader(conn)
	if line, _ := br.ReadString('\n'); !strings.Contains(line, "200") { // read the status line only, then stop reading
		t.Fatalf("status line %q", line)
	}
	waitFor(t, "registration", func() bool { return m.Stats().ActiveConnections == 1 })

	payload := strings.Repeat("x", 8000)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for ctx.Err() == nil && m.Stats().ActiveConnections == 1 {
		_, _ = m.PublishToUser(ctx, "stuck", Event{Data: payload})
		time.Sleep(2 * time.Millisecond)
	}
	if m.Stats().ActiveConnections != 0 {
		t.Fatalf("the stuck client was never cut (slow=%d writeErrors=%d)", m.Stats().SlowClientDisconnect, m.Stats().WriteErrors)
	}
}

func TestSendBufferValidation(t *testing.T) {
	for _, n := range []int{1, 4095, 16<<20 + 1, -1} {
		c := testConfig(func(c *Config) { c.SendBufferBytes = n })
		if err := c.Validate(); err == nil {
			t.Errorf("send_buffer_bytes %d must be refused", n)
		}
	}
	for _, n := range []int{0, 4096, 1 << 20} {
		c := testConfig(func(c *Config) { c.SendBufferBytes = n })
		if err := c.Validate(); err != nil {
			t.Errorf("send_buffer_bytes %d: %v", n, err)
		}
	}
}
