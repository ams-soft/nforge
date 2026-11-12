package nhttp

import (
	"bufio"
	"io"
	stdhttp "net/http"
	"strings"
	"testing"
	"time"
)

// A stream that calls BeginStream outlives every timeout nForge applies to an
// ordinary request (handler, read, write, HTTP/3 stream), on HTTP/1.1, HTTP/2
// and HTTP/3 (a real QUIC client), while an ordinary route is still cut.
func TestBeginStreamOutlivesTheTimeouts(t *testing.T) {
	const limit = 400 * time.Millisecond
	h := newHarness(t, func(c *ServerConfig) {
		c.TLS.Enabled, c.HTTP3.Enabled = true, true
		c.ReadHeaderTimeout, c.ReadTimeout, c.WriteTimeout = limit/2, limit, limit
		c.HandlerTimeout = limit
		c.HTTP3.MaxStreamTimeout = limit
	})
	var ctxErrs = make(chan error, 8)
	h.s.Router().Get("/stream", func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		BeginStream(w, r)
		w.Header().Set("Content-Type", "text/event-stream")
		rc := stdhttp.NewResponseController(w)
		for i := range 12 { // ~1.8 s, several times the limit
			_ = rc.SetWriteDeadline(time.Now().Add(time.Second))
			if _, err := io.WriteString(w, "tick "+string(rune('a'+i))+"\n"); err != nil {
				ctxErrs <- err
				return
			}
			if err := rc.Flush(); err != nil {
				ctxErrs <- err
				return
			}
			select {
			case <-r.Context().Done():
				ctxErrs <- r.Context().Err()
				return
			case <-time.After(150 * time.Millisecond):
			}
		}
		ctxErrs <- nil
	})
	h.s.Router().Get("/ordinary", func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(1500 * time.Millisecond):
			_, _ = io.WriteString(w, "finished")
		}
	})
	h.run(t)

	clients := map[string]struct {
		c    *stdhttp.Client
		url  string
		want int
	}{
		"http/1.1": {&stdhttp.Client{}, addr("http", h.http, "/stream"), 1},
		"http/2":   {h.tcpClient(false), addr("https", h.tls, "/stream"), 2},
		"http/3":   {h.h3Client(), addr("https", h.h3, "/stream"), 3},
	}
	for name, tc := range clients {
		t.Run(name, func(t *testing.T) {
			tc.c.Timeout = 10 * time.Second
			resp := get(t, tc.c, tc.url)
			defer resp.Body.Close()
			if resp.ProtoMajor != tc.want {
				t.Fatalf("proto %d, want %d", resp.ProtoMajor, tc.want)
			}
			sc, lines := bufio.NewScanner(resp.Body), 0
			for sc.Scan() {
				if strings.HasPrefix(sc.Text(), "tick ") {
					lines++
				}
			}
			if lines != 12 {
				t.Fatalf("received %d of 12 ticks (stream cut by a timeout): %v", lines, sc.Err())
			}
			if err := <-ctxErrs; err != nil {
				t.Fatalf("handler saw %v", err)
			}
		})
	}

	// control: without BeginStream the same limits do cut the response
	for name, c := range map[string]*stdhttp.Client{"http/1.1": {Timeout: 5 * time.Second}, "http/3": h.h3Client()} {
		t.Run("ordinary "+name, func(t *testing.T) {
			url := addr("http", h.http, "/ordinary")
			if name == "http/3" {
				url = addr("https", h.h3, "/ordinary")
			}
			if resp, err := c.Get(url); err == nil {
				b, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if string(b) == "finished" {
					t.Fatal("an ordinary route must still be bound by the timeouts")
				}
			}
		})
	}
}
