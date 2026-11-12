package sse

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestFrameFormatting(t *testing.T) {
	m := startManager(t, nil)
	cases := map[string]struct {
		in   wire
		want string
	}{
		"plain":     {wire{ID: "1", Name: "note", Data: "hi"}, "id: 1\nevent: note\ndata: hi\n\n"},
		"no name":   {wire{ID: "1", Data: "hi"}, "id: 1\ndata: hi\n\n"},
		"multiline": {wire{ID: "1", Data: "a\nb\r\nc\rd"}, "id: 1\ndata: a\ndata: b\ndata: c\ndata: d\n\n"},
		"empty":     {wire{ID: "1"}, "id: 1\ndata: \n\n"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := m.frame(tc.in)
			if err != nil || string(got) != tc.want {
				t.Fatalf("frame = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestJSONDataIsEncodedWithTheCodec(t *testing.T) {
	m := startManager(t, nil)
	w, err := m.newWire("u", Event{Name: "n", Data: map[string]any{"a": 1, "b": []string{"x"}}})
	if err != nil || w.Data != `{"a":1,"b":["x"]}` || w.ID == "" {
		t.Fatalf("wire = %+v, %v", w, err)
	}
	if again, _ := m.newWire("u", Event{Data: "x"}); again.ID == w.ID {
		t.Error("generated IDs must differ")
	}
}

func TestEventValidation(t *testing.T) {
	m := startManager(t, func(c *Config) { c.Limits.MaxPayloadBytes = 16 })
	for name, tc := range map[string]struct {
		user string
		ev   Event
		want error
	}{
		"id with LF":       {"u", Event{ID: "a\nb"}, ErrInvalidEvent},
		"id with CR":       {"u", Event{ID: "a\rb"}, ErrInvalidEvent},
		"id with NUL":      {"u", Event{ID: "a\x00b"}, ErrInvalidEvent},
		"id too long":      {"u", Event{ID: strings.Repeat("a", 129)}, ErrInvalidEvent},
		"name with LF":     {"u", Event{Name: "a\nb"}, ErrInvalidEvent},
		"name with space":  {"u", Event{Name: "a b"}, ErrInvalidEvent},
		"name too long":    {"u", Event{Name: strings.Repeat("a", 65)}, ErrInvalidEvent},
		"data too large":   {"u", Event{Data: strings.Repeat("a", 17)}, ErrPayloadTooLarge},
		"data invalid utf": {"u", Event{Data: []byte{0xff, 0xfe}}, ErrInvalidEvent},
		"unencodable":      {"u", Event{Data: make(chan int)}, ErrInvalidEvent},
		"empty user":       {"", Event{}, ErrInvalidUser},
		"user with LF":     {"a\nb", Event{}, ErrInvalidUser},
		"user too long":    {strings.Repeat("u", 513), Event{}, ErrInvalidUser},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := m.PublishToUser(t.Context(), tc.user, tc.ev); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
	if res, err := m.PublishToUser(t.Context(), "u", Event{ID: "ok", Name: "a.b:c-d_e", Data: "x"}); err != nil || !res.Accepted {
		t.Fatalf("a valid event: %+v %v", res, err)
	}
}

func TestStreamHeadersAndDelivery(t *testing.T) {
	m := startManager(t, nil)
	srv := serverFor(t, m)
	c := openStream(t, srv.URL, "alice")
	h := c.resp.Header
	if c.resp.StatusCode != 200 || h.Get("Content-Type") != "text/event-stream; charset=utf-8" ||
		h.Get("Cache-Control") != "no-cache, no-transform" || h.Get("X-Accel-Buffering") != "no" {
		t.Fatalf("status %d headers %v", c.resp.StatusCode, h)
	}
	first, ok := c.frame(time.Second)
	if !ok || !strings.HasPrefix(first, "retry: 3000\n") {
		t.Fatalf("first frame %q: the stream must flush promptly and advertise retry", first)
	}
	waitFor(t, "registration", func() bool { return m.Stats().ActiveConnections == 1 })
	if res, err := m.PublishToUser(t.Context(), "alice", Event{ID: "e1", Name: "greet", Data: map[string]string{"hello": "world"}}); err != nil || !res.Accepted {
		t.Fatal(res, err)
	}
	ev, ok := c.event(2 * time.Second)
	if !ok || ev != "id: e1\nevent: greet\ndata: {\"hello\":\"world\"}\n\n" {
		t.Fatalf("event = %q", ev)
	}
}

func TestMultipleConnectionsAndUserIsolation(t *testing.T) {
	m := startManager(t, nil)
	srv := serverFor(t, m)
	a1, a2 := openStream(t, srv.URL, "alice"), openStream(t, srv.URL, "alice")
	b := openStream(t, srv.URL, "bob")
	waitFor(t, "three streams", func() bool { return m.Stats().ActiveConnections == 3 })
	st := m.Stats()
	if st.ActiveUsers != 2 || st.ConnectionsPerUser[2] != 1 || st.ConnectionsPerUser[1] != 1 {
		t.Fatalf("stats %+v", st)
	}
	if _, err := m.PublishToUser(t.Context(), "alice", Event{ID: "x", Data: "for alice"}); err != nil {
		t.Fatal(err)
	}
	for i, c := range []*client{a1, a2} {
		if ev, ok := c.event(2 * time.Second); !ok || !strings.Contains(ev, "for alice") {
			t.Fatalf("alice stream %d: %q %v", i, ev, ok)
		}
	}
	if ev, ok := b.event(300 * time.Millisecond); ok {
		t.Fatalf("bob received alice's event: %q", ev)
	}
}

func TestOfflineEventsAreDiscarded(t *testing.T) {
	m := startManager(t, nil)
	srv := serverFor(t, m)
	res, err := m.PublishToUser(t.Context(), "carol", Event{Data: "lost"})
	if err != nil || !res.Accepted {
		t.Fatalf("publishing to an absent user is a successful no-op: %+v %v", res, err)
	}
	c := openStream(t, srv.URL, "carol", "Last-Event-ID", "42")
	if ev, ok := c.event(300 * time.Millisecond); ok {
		t.Fatalf("an event published while offline was replayed: %q", ev)
	}
}

func TestIdentityErrors(t *testing.T) {
	m := startManager(t, nil)
	for name, tc := range map[string]struct {
		resolver IdentityResolver
		want     int
	}{
		"unauthorized": {func(*http.Request) (string, error) { return "", ErrUnauthorized }, 401},
		"other error":  {func(*http.Request) (string, error) { return "", errors.New("boom") }, 401},
		"forbidden":    {func(*http.Request) (string, error) { return "", fmt.Errorf("x: %w", ErrForbidden) }, 403},
		"bad user id":  {func(*http.Request) (string, error) { return "a\nb", nil }, 403},
	} {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			m.Handler(WithIdentityResolver(tc.resolver)).ServeHTTP(rec, httptest.NewRequest("GET", "/s", nil))
			if rec.Code != tc.want || strings.Contains(rec.Header().Get("Content-Type"), "event-stream") {
				t.Fatalf("status %d content-type %q, want %d without SSE headers", rec.Code, rec.Header().Get("Content-Type"), tc.want)
			}
		})
	}
	// the default resolver refuses a request nothing authenticated
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/s", nil))
	if rec.Code != 401 {
		t.Fatalf("default resolver without a principal: %d", rec.Code)
	}
	if m.Stats().ActiveConnections != 0 {
		t.Error("a refused request must not register a connection")
	}
}

func TestRequestValidation(t *testing.T) {
	m := startManager(t, nil)
	h := m.Handler(WithIdentityResolver(headerIdentity))
	do := func(method string, hdr ...string) int {
		req := httptest.NewRequest(method, "/s", nil)
		req.Header.Set("X-Test-User", "u")
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		rec := httptest.NewRecorder()
		ctx, cancel := context.WithCancel(req.Context())
		cancel() // an accepted stream ends at once
		h.ServeHTTP(rec, req.WithContext(ctx))
		return rec.Code
	}
	if got := do("POST"); got != 405 {
		t.Errorf("POST = %d", got)
	}
	if got := do("GET", "Last-Event-ID", strings.Repeat("a", 129)); got != 400 {
		t.Errorf("oversized Last-Event-ID = %d", got)
	}
	if got := do("GET", "Last-Event-ID", "a\x01b"); got != 400 {
		t.Errorf("control char Last-Event-ID = %d", got)
	}
	if got := do("GET", "Last-Event-ID", "ok-7"); got != 200 {
		t.Errorf("valid Last-Event-ID = %d", got)
	}
}

func TestOriginPolicy(t *testing.T) {
	check := func(allowed []string, origin, host string) int {
		m := startManager(t, func(c *Config) { c.AllowedOrigins = allowed })
		req := httptest.NewRequest("GET", "http://"+host+"/s", nil)
		req.Header.Set("X-Test-User", "u")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		ctx, cancel := context.WithCancel(req.Context())
		cancel()
		rec := httptest.NewRecorder()
		m.Handler(WithIdentityResolver(headerIdentity)).ServeHTTP(rec, req.WithContext(ctx))
		return rec.Code
	}
	for name, tc := range map[string]struct {
		allowed      []string
		origin, host string
		want         int
	}{
		"no origin":              {nil, "", "api.test", 200},
		"same origin":            {nil, "https://api.test", "api.test", 200},
		"cross origin, no list":  {nil, "https://evil.test", "api.test", 403},
		"listed origin":          {[]string{"https://app.test"}, "https://app.test", "api.test", 200},
		"unlisted origin":        {[]string{"https://app.test"}, "https://evil.test", "api.test", 403},
		"own origin not in list": {[]string{"https://app.test"}, "https://api.test", "api.test", 403},
		"null origin":            {nil, "null", "api.test", 403},
	} {
		t.Run(name, func(t *testing.T) {
			if got := check(tc.allowed, tc.origin, tc.host); got != tc.want {
				t.Fatalf("status %d, want %d", got, tc.want)
			}
		})
	}
}

func TestConnectionLimits(t *testing.T) {
	m := startManager(t, func(c *Config) { c.MaxConnections = 3; c.MaxConnectionsPerUser = 2 })
	srv := serverFor(t, m)
	status := func(user string) int {
		req, _ := http.NewRequestWithContext(t.Context(), "GET", srv.URL, nil)
		req.Header.Set("X-Test-User", user)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { resp.Body.Close() })
		return resp.StatusCode
	}
	for range 2 {
		if got := status("a"); got != 200 {
			t.Fatalf("within the limit: %d", got)
		}
	}
	if got := status("a"); got != 429 {
		t.Errorf("per-user limit = %d, want 429", got)
	}
	if got := status("b"); got != 200 {
		t.Fatalf("another user is unaffected: %d", got)
	}
	if got := status("c"); got != 503 {
		t.Errorf("global limit = %d, want 503", got)
	}
	if m.Stats().ConnectionsRejected != 2 {
		t.Errorf("rejected = %d", m.Stats().ConnectionsRejected)
	}
}

func TestConnectionRateLimit(t *testing.T) {
	m := startManager(t, func(c *Config) { c.MaxConnectsPerSecond = 2 })
	h := m.Handler(WithIdentityResolver(headerIdentity))
	var limited int
	for range 6 {
		req := httptest.NewRequest("GET", "/s", nil)
		req.Header.Set("X-Test-User", "u")
		ctx, cancel := context.WithCancel(req.Context())
		cancel()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req.WithContext(ctx))
		if rec.Code == 429 {
			limited++
			if rec.Header().Get("Retry-After") == "" {
				t.Error("429 needs Retry-After")
			}
		}
	}
	if limited == 0 {
		t.Fatal("the connection rate was not limited")
	}
}

func TestSlowClientIsDisconnectedNotPartiallyFed(t *testing.T) {
	m := startManager(t, func(c *Config) { c.BufferSize = 2 })
	// a connection nobody drains: queue of 2, the third event overflows it
	c := newConn("slow", 2)
	if err := m.register(c); err != nil {
		t.Fatal(err)
	}
	defer m.unregister(c)
	for i := range 3 {
		m.deliver(wire{User: "slow", ID: fmt.Sprint(i), Data: "x"})
	}
	select {
	case <-c.done:
	default:
		t.Fatal("a full queue must disconnect the client")
	}
	if c.reason != "queue_full" || m.Stats().SlowClientDisconnect != 1 {
		t.Fatalf("reason %q, slow %d", c.reason, m.Stats().SlowClientDisconnect)
	}
	// later events skip the closed connection instead of counting again
	m.deliver(wire{User: "slow", ID: "9", Data: "x"})
	if m.Stats().SlowClientDisconnect != 1 {
		t.Error("a disconnected client must not be disconnected again")
	}
}

func TestHeartbeat(t *testing.T) {
	m := startManager(t, func(c *Config) { c.Heartbeat = 30 * time.Millisecond })
	c := openStream(t, serverFor(t, m).URL, "u")
	beats := 0
	for range 6 {
		f, ok := c.frame(time.Second)
		if !ok {
			t.Fatal("stream ended")
		}
		if f == ": heartbeat\n\n" {
			beats++
		}
	}
	if beats < 3 {
		t.Fatalf("only %d heartbeats", beats)
	}
}

func TestMaxLifetimeEndsTheStream(t *testing.T) {
	m := startManager(t, func(c *Config) { c.MaxLifetime = 80 * time.Millisecond })
	c := openStream(t, serverFor(t, m).URL, "u")
	if !c.ended(3 * time.Second) {
		t.Fatal("the stream outlived max_lifetime")
	}
	waitFor(t, "unregister", func() bool { return m.Stats().ActiveConnections == 0 })
	if m.Stats().LifetimeCloses != 1 {
		t.Errorf("lifetime closes = %d", m.Stats().LifetimeCloses)
	}
}

func TestClientDisconnectReleasesTheConnection(t *testing.T) {
	m := startManager(t, nil)
	srv := serverFor(t, m)
	ctx, cancel := context.WithCancel(t.Context())
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
	req.Header.Set("X-Test-User", "u")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "registration", func() bool { return m.Stats().ActiveConnections == 1 })
	cancel()
	resp.Body.Close()
	waitFor(t, "unregistration", func() bool { return m.Stats().ActiveConnections == 0 })
}

// Publishers, joiners and leavers race; run with -race.
func TestPublishCloseRaces(t *testing.T) {
	m := startManager(t, func(c *Config) { c.BufferSize = 4; c.MaxConnectionsPerUser = 1000; c.MaxConnections = 100000 })
	var wg sync.WaitGroup
	stop := make(chan struct{})
	var published atomic.Int64
	for range 4 {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				if _, err := m.PublishToUser(context.Background(), "u", Event{Data: "x"}); err == nil {
					published.Add(1)
				}
			}
		})
	}
	for range 4 {
		wg.Go(func() {
			for range 300 {
				c := newConn("u", 4)
				if m.register(c) != nil {
					continue
				}
				if published.Load()%2 == 0 {
					c.close("test")
				}
				m.unregister(c)
				c.close("test")
			}
		})
	}
	time.Sleep(100 * time.Millisecond)
	close(stop)
	wg.Wait()
	if st := m.Stats(); st.ActiveConnections != 0 || st.ActiveUsers != 0 {
		t.Fatalf("leaked connections: %+v", st)
	}
}

func TestShutdownClosesStreamsAndIsIdempotent(t *testing.T) {
	m := newManager(testConfig(nil), testRegistry(t), nil, &memoryDriver{})
	if err := m.start(t.Context()); err != nil {
		t.Fatal(err)
	}
	srv := serverFor(t, m)
	c1, c2 := openStream(t, srv.URL, "a"), openStream(t, srv.URL, "b")
	waitFor(t, "streams", func() bool { return m.Stats().ActiveConnections == 2 })

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for range 3 {
		wg.Go(func() {
			if err := m.close(ctx); err != nil {
				t.Errorf("close: %v", err)
			}
		})
	}
	wg.Wait()
	if !c1.ended(2*time.Second) || !c2.ended(2*time.Second) {
		t.Fatal("open streams must end at shutdown")
	}
	if _, err := m.PublishToUser(ctx, "a", Event{}); !errors.Is(err, ErrClosed) {
		t.Errorf("publish after close: %v", err)
	}
	rec := httptest.NewRecorder()
	m.Handler(WithIdentityResolver(headerIdentity)).ServeHTTP(rec, func() *http.Request {
		r := httptest.NewRequest("GET", "/s", nil)
		r.Header.Set("X-Test-User", "a")
		return r
	}())
	if rec.Code != 503 {
		t.Errorf("a stream after close = %d, want 503", rec.Code)
	}
}

func TestNotAcceptingBeforeStart(t *testing.T) {
	m := newManager(testConfig(nil), testRegistry(t), nil, &memoryDriver{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/s", nil)
	req.Header.Set("X-Test-User", "u")
	m.Handler(WithIdentityResolver(headerIdentity)).ServeHTTP(rec, req)
	if rec.Code != 503 || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("before start: %d %v", rec.Code, rec.Header())
	}
	if _, err := m.PublishToUser(t.Context(), "u", Event{}); !errors.Is(err, ErrClosed) {
		t.Fatalf("publish before start: %v", err)
	}
}

func TestStreamingUnsupportedIsRefusedBeforeHeaders(t *testing.T) {
	m := startManager(t, nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/s", nil)
	req.Header.Set("X-Test-User", "u")
	m.Handler(WithIdentityResolver(headerIdentity)).ServeHTTP(noFlush{rec}, req)
	if rec.Code != 500 || strings.Contains(rec.Header().Get("Content-Type"), "event-stream") {
		t.Fatalf("status %d headers %v", rec.Code, rec.Header())
	}
}

// noFlush hides the recorder's Flush.
type noFlush struct{ rec *httptest.ResponseRecorder }

func (n noFlush) Header() http.Header         { return n.rec.Header() }
func (n noFlush) Write(b []byte) (int, error) { return n.rec.Write(b) }
func (n noFlush) WriteHeader(c int)           { n.rec.WriteHeader(c) }

func TestHTTP2StreamHasNoConnectionHeader(t *testing.T) {
	m := startManager(t, nil)
	srv := httptest.NewUnstartedServer(m.Handler(WithIdentityResolver(headerIdentity)))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	req, _ := http.NewRequestWithContext(t.Context(), "GET", srv.URL, nil)
	req.Header.Set("X-Test-User", "u")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.ProtoMajor != 2 || resp.Header.Get("Connection") != "" {
		t.Fatalf("proto %d, Connection %q", resp.ProtoMajor, resp.Header.Get("Connection"))
	}
	waitFor(t, "registration", func() bool { return m.Stats().ActiveConnections == 1 })
	if _, err := m.PublishToUser(t.Context(), "u", Event{ID: "h2", Data: "over h2"}); err != nil {
		t.Fatal(err)
	}
	c := &client{resp: resp, t: t}
	c.rd = newReader(resp)
	if ev, ok := c.event(2 * time.Second); !ok || !strings.Contains(ev, "over h2") {
		t.Fatalf("h2 event %q", ev)
	}
}

func TestConfigValidation(t *testing.T) {
	for name, mut := range map[string]func(*Config){
		"driver":          func(c *Config) { c.Driver = "kafka" },
		"heartbeat":       func(c *Config) { c.Heartbeat = time.Millisecond },
		"buffer":          func(c *Config) { c.BufferSize = -1 },
		"per user > max":  func(c *Config) { c.MaxConnectionsPerUser = c.MaxConnections + 1 },
		"wildcard origin": func(c *Config) { c.AllowedOrigins = []string{"*"} },
		"origin path":     func(c *Config) { c.AllowedOrigins = []string{"https://a.test/"} },
		"lifetime":        func(c *Config) { c.MaxLifetime = time.Second },
	} {
		t.Run(name, func(t *testing.T) {
			c := testConfig(mut)
			if err := c.Validate(); err == nil {
				t.Fatal("want a validation error")
			}
		})
	}
	if err := testConfig(nil).Validate(); err != nil {
		t.Fatalf("defaults must validate: %v", err)
	}
}
