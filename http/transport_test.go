package nhttp

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"net"
	stdhttp "net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

// writeCert writes a throwaway self-signed pair for 127.0.0.1/localhost and
// returns its paths and a pool that trusts it. Test-only: nothing reuses it.
func writeCert(t *testing.T) (certFile, keyFile string, pool *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "nforge-test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kd, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile, keyFile = filepath.Join(dir, "c.crt"), filepath.Join(dir, "c.key")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kd}), 0o600); err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	pool = x509.NewCertPool()
	pool.AddCert(cert)
	return certFile, keyFile, pool
}

// freePort returns a port free for both TCP and UDP (best effort: it is
// released before use).
func freePort(t *testing.T) int {
	t.Helper()
	for range 50 {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := ln.Addr().(*net.TCPAddr).Port
		_ = ln.Close()
		pc, err := net.ListenPacket("udp", "127.0.0.1:"+strconv.Itoa(port))
		if err != nil {
			continue
		}
		_ = pc.Close()
		return port
	}
	t.Fatal("no free port")
	return 0
}

type harness struct {
	s    *Server
	pool *x509.CertPool
	hits atomic.Int32
	http int
	tls  int
	h3   int

	certFile, keyFile string
}

// start builds a server whose config comes from mutate (on top of a TLS pair
// and fresh ports), registers /ping and starts it.
func start(t *testing.T, mutate func(c *ServerConfig)) *harness {
	t.Helper()
	h := newHarness(t, mutate)
	h.run(t)
	return h
}

func newHarness(t *testing.T, mutate func(c *ServerConfig)) *harness {
	t.Helper()
	certFile, keyFile, pool := writeCert(t)
	h := &harness{pool: pool, http: freePort(t), tls: freePort(t), h3: freePort(t), certFile: certFile, keyFile: keyFile}
	var cfg testConfig
	cfg.HTTP.Server = ServerConfig{Host: "127.0.0.1", Port: h.http}
	cfg.HTTP.Server.TLS = ServerTLSConfig{Port: h.tls, CertFile: certFile, KeyFile: keyFile}
	cfg.HTTP.Server.HTTP3 = ServerHTTP3Config{Port: h.h3}
	mutate(&cfg.HTTP.Server)
	h.s = NewHTTPServer(&testDeps{Config: cfg, Logger: slog.New(slog.DiscardHandler)})
	h.s.Router().Get("/ping", func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		h.hits.Add(1)
		_, _ = io.WriteString(w, r.Proto)
	})
	h.s.Router().Post("/echo", func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		h.hits.Add(1)
		_, _ = io.Copy(w, r.Body)
	})
	return h
}

// run starts the server: Start returns once every listener is bound.
func (h *harness) run(t *testing.T) {
	t.Helper()
	if err := h.s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.stop(t) })
}

// stop drains and closes every transport; later calls return nil.
func (h *harness) stop(t *testing.T) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return h.s.Stop(ctx)
}

func (h *harness) tcpClient(redirects bool) *stdhttp.Client {
	c := &stdhttp.Client{
		Timeout:   5 * time.Second,
		Transport: &stdhttp.Transport{TLSClientConfig: &tls.Config{RootCAs: h.pool}, ForceAttemptHTTP2: true},
	}
	if !redirects {
		c.CheckRedirect = func(*stdhttp.Request, []*stdhttp.Request) error { return stdhttp.ErrUseLastResponse }
	}
	return c
}

func (h *harness) h3Client() *stdhttp.Client {
	return &stdhttp.Client{Timeout: 5 * time.Second, Transport: &http3.Transport{TLSClientConfig: &tls.Config{RootCAs: h.pool}}}
}

// get retries until the listener is up (the server starts in a goroutine).
func get(t *testing.T, c *stdhttp.Client, url string) *stdhttp.Response {
	t.Helper()
	var err error
	for range 100 {
		var resp *stdhttp.Response
		if resp, err = c.Get(url); err == nil {
			return resp
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("GET %s: %v", url, err)
	return nil
}

func body(t *testing.T, r *stdhttp.Response) string {
	t.Helper()
	defer r.Body.Close()
	b, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func addr(scheme string, port int, path string) string {
	return scheme + "://127.0.0.1:" + strconv.Itoa(port) + path
}

func refused(port int) bool {
	c, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), time.Second)
	if err == nil {
		_ = c.Close()
	}
	return err != nil
}

func TestZeroConfigDefaultsTo8080(t *testing.T) {
	s := NewHTTPServer(&testDeps{Logger: slog.New(slog.DiscardHandler)})
	if s.Srv.Addr != ":8080" {
		t.Fatalf("Addr = %q, want :8080", s.Srv.Addr)
	}
}

func TestAllTransportsShareTheHandler(t *testing.T) {
	h := start(t, func(c *ServerConfig) {
		c.TLS.Enabled, c.HTTP3.Enabled = true, true
	})
	if got := body(t, get(t, &stdhttp.Client{Timeout: 5 * time.Second}, addr("http", h.http, "/ping"))); got != "HTTP/1.1" {
		t.Errorf("http proto = %q", got)
	}
	resp := get(t, h.tcpClient(true), addr("https", h.tls, "/ping"))
	if resp.TLS == nil || resp.TLS.NegotiatedProtocol != "h2" || resp.TLS.Version != tls.VersionTLS13 {
		t.Errorf("https: tls=%+v", resp.TLS)
	}
	if as := resp.Header.Get("Alt-Svc"); !strings.Contains(as, `h3=":`+strconv.Itoa(h.h3)+`"`) {
		t.Errorf("Alt-Svc = %q, want the UDP port %d", as, h.h3)
	}
	if got := body(t, resp); got != "HTTP/2.0" {
		t.Errorf("https proto = %q", got)
	}
	resp = get(t, h.h3Client(), addr("https", h.h3, "/ping"))
	if resp.ProtoMajor != 3 || resp.TLS == nil || resp.TLS.Version != tls.VersionTLS13 {
		t.Errorf("h3: proto=%s tls=%+v", resp.Proto, resp.TLS)
	}
	if body(t, resp) != "HTTP/3.0" {
		t.Error("handler did not see HTTP/3")
	}
	if resp.Header.Get("Alt-Svc") != "" {
		t.Errorf("Alt-Svc must only be sent on HTTPS responses, got %q", resp.Header.Get("Alt-Svc"))
	}
	// request bodies work on every transport
	for _, c := range []struct {
		cl  *stdhttp.Client
		url string
	}{{h.tcpClient(true), addr("https", h.tls, "/echo")}, {h.h3Client(), addr("https", h.h3, "/echo")}} {
		r, err := c.cl.Post(c.url, "text/plain", strings.NewReader("hello"))
		if err != nil || body(t, r) != "hello" {
			t.Errorf("echo %s: %v", c.url, err)
		}
	}
}

func TestMinTLSVersion(t *testing.T) {
	for _, tc := range []struct {
		min    string
		client uint16
		ok     bool
	}{{"", tls.VersionTLS12, false}, {"1.3", tls.VersionTLS12, false}, {"1.2", tls.VersionTLS12, true}} {
		h := start(t, func(c *ServerConfig) { c.TLS.Enabled, c.TLS.MinVersion = true, tc.min })
		get(t, h.tcpClient(true), addr("https", h.tls, "/ping")).Body.Close() // wait for startup with a TLS 1.3 client
		cl := h.tcpClient(true)
		cl.Transport.(*stdhttp.Transport).TLSClientConfig.MaxVersion = tc.client
		_, err := cl.Get(addr("https", h.tls, "/ping"))
		if (err == nil) != tc.ok {
			t.Errorf("min=%q client max=%x: err=%v, want ok=%v", tc.min, tc.client, err, tc.ok)
		}
	}
}

func TestRedirectOnlyListener(t *testing.T) {
	h := start(t, func(c *ServerConfig) {
		c.TLS.Enabled, c.TLS.RedirectHTTP = true, true
	})
	get(t, h.tcpClient(true), addr("https", h.tls, "/ping")).Body.Close()
	h.hits.Store(0)
	cl := h.tcpClient(false)

	req, _ := stdhttp.NewRequest("POST", addr("http", h.http, "/echo?a=b%20c&x=1"), strings.NewReader("data"))
	req.Host = "evil.example" // a forged Host must not reach Location
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	want := "https://127.0.0.1:" + strconv.Itoa(h.tls) + "/echo?a=b%20c&x=1"
	if resp.StatusCode != 308 || resp.Header.Get("Location") != want {
		t.Fatalf("status=%d Location=%q, want 308 %q", resp.StatusCode, resp.Header.Get("Location"), want)
	}
	// application, docs, health and unknown paths never reach a handler
	for _, p := range []string{"/ping", "/docs", "/health", "/.well-known/jwks.json", "/nope"} {
		r, err := cl.Get(addr("http", h.http, p))
		if err != nil || r.StatusCode != 308 {
			t.Errorf("%s: %v %v", p, r, err)
		}
	}
	if h.hits.Load() != 0 {
		t.Errorf("application handlers ran %d times on the redirect listener", h.hits.Load())
	}
}

func TestRedirectHandlerEdgeCases(t *testing.T) {
	h := redirectHandler("example.com")
	for _, tc := range []struct{ target, want string }{
		{"/a//b", "https://example.com/a//b"},
		{"//evil.com/x", "https://example.com//evil.com/x"},
		{"/p%2Fq?z=%0d%0aSet-Cookie:x", "https://example.com/p%2Fq?z=%0d%0aSet-Cookie:x"},
	} {
		rec := &recorder{h: stdhttp.Header{}}
		h.ServeHTTP(rec, newReq(t, tc.target))
		if rec.code != 308 || rec.h.Get("Location") != tc.want {
			t.Errorf("%q → %d %q, want %q", tc.target, rec.code, rec.h.Get("Location"), tc.want)
		}
	}
}

func TestHTTPDisabled(t *testing.T) {
	h := start(t, func(c *ServerConfig) { c.TLS.Enabled, c.TLS.DisableInsecureHTTP = true, true })
	get(t, h.tcpClient(true), addr("https", h.tls, "/ping")).Body.Close()
	if !refused(h.http) {
		t.Error("the plain HTTP port is open")
	}
}

func TestHTTP3Alone(t *testing.T) {
	certFile, keyFile, _ := writeCert(t)
	h := newHarness(t, func(c *ServerConfig) {
		c.TLS.CertFile, c.TLS.KeyFile = "", ""
		c.TLS.DisableInsecureHTTP = true
		c.HTTP3 = ServerHTTP3Config{Enabled: true, Port: c.HTTP3.Port, SameKeyCertTLS: new(false), CertFile: certFile, KeyFile: keyFile}
	})
	pool := x509.NewCertPool()
	raw, _ := os.ReadFile(h.s.cfg.Server.HTTP3.CertFile)
	blk, _ := pem.Decode(raw)
	crt, _ := x509.ParseCertificate(blk.Bytes)
	pool.AddCert(crt)
	h.pool = pool
	h.run(t)
	resp := get(t, h.h3Client(), addr("https", h.h3, "/ping"))
	if resp.ProtoMajor != 3 {
		t.Errorf("proto = %s", resp.Proto)
	}
	resp.Body.Close()
	if !refused(h.http) || !refused(h.tls) {
		t.Error("a TCP listener is open in HTTP/3-only mode")
	}
}

func TestAltSvcDisabled(t *testing.T) {
	h := start(t, func(c *ServerConfig) {
		c.TLS.Enabled, c.HTTP3.Enabled, c.HTTP3.Advertise = true, true, new(false)
	})
	resp := get(t, h.tcpClient(true), addr("https", h.tls, "/ping"))
	defer resp.Body.Close()
	if resp.Header.Get("Alt-Svc") != "" {
		t.Errorf("Alt-Svc = %q with advertise: false", resp.Header.Get("Alt-Svc"))
	}
}

func TestStartupFailuresLeaveNothingRunning(t *testing.T) {
	t.Run("bad certificate", func(t *testing.T) {
		h := newHarness(t, func(c *ServerConfig) {
			c.TLS.Enabled = true
			c.TLS.KeyFile = c.TLS.CertFile // not a key
		})
		err := h.s.Start(context.Background())
		if err == nil || !strings.Contains(err.Error(), "certificate") {
			t.Fatalf("err = %v", err)
		}
		if !refused(h.http) || !refused(h.tls) {
			t.Error("a listener stayed open")
		}
	})
	t.Run("occupied UDP port", func(t *testing.T) {
		h := newHarness(t, func(c *ServerConfig) { c.TLS.Enabled, c.HTTP3.Enabled = true, true })
		pc, err := net.ListenPacket("udp", "127.0.0.1:"+strconv.Itoa(h.h3))
		if err != nil {
			t.Fatal(err)
		}
		defer pc.Close()
		if err := h.s.Start(context.Background()); err == nil {
			t.Fatal("expected a bind error")
		}
		if !refused(h.http) || !refused(h.tls) {
			t.Error("a TCP listener stayed open")
		}
	})
	t.Run("occupied HTTP port", func(t *testing.T) {
		h := newHarness(t, func(c *ServerConfig) { c.TLS.Enabled, c.HTTP3.Enabled = true, true })
		ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(h.http))
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		if err := h.s.Start(context.Background()); err == nil {
			t.Fatal("expected a bind error")
		}
		if !refused(h.tls) {
			t.Error("HTTPS stayed open")
		}
		pc, err := net.ListenPacket("udp", "127.0.0.1:"+strconv.Itoa(h.h3))
		if err != nil {
			t.Errorf("UDP port still held: %v", err)
		} else {
			pc.Close()
		}
	})
	t.Run("invalid config", func(t *testing.T) {
		h := newHarness(t, func(c *ServerConfig) { c.TLS.RedirectHTTP = true })
		if err := h.s.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "redirect_http") {
			t.Fatalf("err = %v", err)
		}
	})
}

// Stop returns only once every listener is released, so the module can close
// what the handlers use right after it.
func TestStopReleasesEveryListener(t *testing.T) {
	h := newHarness(t, func(c *ServerConfig) { c.TLS.Enabled, c.HTTP3.Enabled = true, true })
	h.run(t)
	get(t, h.h3Client(), addr("https", h.h3, "/ping")).Body.Close()
	if err := h.stop(t); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	for _, p := range []int{h.http, h.tls} {
		ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(p))
		if err != nil {
			t.Errorf("tcp %d still bound: %v", p, err)
		} else {
			ln.Close()
		}
	}
	pc, err := net.ListenPacket("udp", "127.0.0.1:"+strconv.Itoa(h.h3))
	if err != nil {
		t.Errorf("udp still bound: %v", err)
	} else {
		pc.Close()
	}
	if err := h.s.Start(context.Background()); err == nil {
		t.Fatal("a server starts once")
	}
}

func TestConcurrentTrafficAndShutdown(t *testing.T) {
	h := start(t, func(c *ServerConfig) { c.TLS.Enabled, c.HTTP3.Enabled = true, true })
	get(t, h.h3Client(), addr("https", h.h3, "/ping")).Body.Close()
	clients := []struct {
		c *stdhttp.Client
		u string
	}{
		{&stdhttp.Client{Timeout: 5 * time.Second}, addr("http", h.http, "/ping")},
		{h.tcpClient(true), addr("https", h.tls, "/ping")},
		{h.h3Client(), addr("https", h.h3, "/ping")},
	}
	var wg sync.WaitGroup
	for range 20 {
		for _, c := range clients {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if r, err := c.c.Get(c.u); err == nil {
					_, _ = io.Copy(io.Discard, r.Body)
					r.Body.Close()
				}
			}()
		}
	}
	// Shutdown during traffic must finish within its bound. net/http can leave
	// an HTTP/2 connection accepted while Shutdown started waiting until the
	// drain deadline (then forcibly closed), which surfaces as a deadline
	// error: that is the documented, bounded outcome, not a leak.
	if err := h.stop(t); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("shutdown: %v", err)
	}
	wg.Wait()
}

func TestHTTP3StreamTimeout(t *testing.T) {
	h := newHarness(t, func(c *ServerConfig) {
		c.TLS.Enabled, c.HTTP3.Enabled = true, true
		c.HTTP3.MaxStreamTimeout = 200 * time.Millisecond
	})
	var tcpErr, h3Err atomic.Value
	var h3Elapsed atomic.Int64
	h.s.Router().Get("/block", func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		start := time.Now()
		select {
		case <-r.Context().Done():
			if r.ProtoMajor == 3 {
				h3Err.Store(r.Context().Err())
				h3Elapsed.Store(int64(time.Since(start)))
			} else {
				tcpErr.Store(r.Context().Err())
			}
		case <-time.After(2 * time.Second):
		}
	})
	h.run(t)
	// the write deadline passes while the handler waits, so the client may see
	// the stream reset instead of a response: only the handler matters here
	get(t, &stdhttp.Client{Timeout: 5 * time.Second}, addr("http", h.http, "/ping")).Body.Close()
	if r, err := h.h3Client().Get(addr("https", h.h3, "/block")); err == nil {
		r.Body.Close()
	}
	if err, _ := h3Err.Load().(error); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("HTTP/3 handler context = %v, want deadline exceeded", err)
	}
	if d := time.Duration(h3Elapsed.Load()); d > time.Second {
		t.Errorf("deadline fired after %s, want ~200ms", d)
	}
}

func TestQUICIdleTimeoutApplied(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for a real QUIC idle timeout (~6 s)")
	}
	// The negotiated idle timeout is the smaller of both sides: a client that
	// would idle for 30 s sees the connection die at the server's 6 s. (quic-go
	// does not honor values under ~5 s on an idle connection in practice, so a
	// smaller one would not prove the setting is applied.)
	const idle = 6 * time.Second
	h := start(t, func(c *ServerConfig) {
		c.TLS.Enabled, c.HTTP3.Enabled = true, true
		c.HTTP3.MaxIdleTimeout = idle
	})
	get(t, h.h3Client(), addr("https", h.h3, "/ping")).Body.Close() // server is up
	dctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := quic.DialAddr(dctx, "127.0.0.1:"+strconv.Itoa(h.h3),
		&tls.Config{RootCAs: h.pool, NextProtos: []string{http3.NextProtoH3}, ServerName: "localhost"},
		&quic.Config{MaxIdleTimeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	select {
	case <-conn.Context().Done():
		if d := time.Since(start); d < idle-time.Second || d > idle+3*time.Second {
			t.Errorf("closed after %s, want about %s", d, idle)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("connection still open long after the configured idle timeout")
	}
}

type recorder struct {
	h    stdhttp.Header
	code int
}

func (r *recorder) Header() stdhttp.Header      { return r.h }
func (r *recorder) Write(b []byte) (int, error) { return len(b), nil }
func (r *recorder) WriteHeader(code int)        { r.code = code }

func newReq(t *testing.T, target string) *stdhttp.Request {
	t.Helper()
	req, err := stdhttp.ReadRequest(bufioReader("GET " + target + " HTTP/1.1\r\nHost: x\r\n\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	return req
}

// Settings the application put on the exported Srv before Start apply to every
// transport, not only to the plain HTTP listener.
func TestSrvCustomisationReachesEveryTransport(t *testing.T) {
	var states atomic.Int32
	h := newHarness(t, func(c *ServerConfig) { c.HTTP3.Enabled, c.TLS.Enabled = true, true })
	h.s.Srv.MaxHeaderBytes = 2048
	h.s.Srv.ConnState = func(net.Conn, stdhttp.ConnState) { states.Add(1) }
	h.run(t)

	// well above the limit: net/http does not count header bytes exactly on a
	// reused connection, so a value just over 2048+4096 can still pass
	big := strings.Repeat("a", 20000)
	for name, c := range map[string]struct {
		client *stdhttp.Client
		url    string
	}{
		"http":  {h.tcpClient(false), addr("http", h.http, "/ping")},
		"https": {h.tcpClient(false), addr("https", h.tls, "/ping")},
		"h3":    {h.h3Client(), addr("https", h.h3, "/ping")},
	} {
		t.Run(name, func(t *testing.T) {
			// the request must get through the listener first
			get(t, c.client, c.url).Body.Close()
			req, _ := stdhttp.NewRequest("GET", c.url, nil)
			req.Header.Set("X-Big", big)
			resp, err := c.client.Do(req)
			if err != nil && strings.Contains(err.Error(), "larger than peer's advertised limit") {
				return // HTTP/2: the client refused to send what the server said it would not accept
			}
			if err != nil {
				t.Fatalf("%v", err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != stdhttp.StatusRequestHeaderFieldsTooLarge {
				t.Fatalf("status %d, want 431 with MaxHeaderBytes = 2048", resp.StatusCode)
			}
		})
	}
	if states.Load() == 0 {
		t.Error("ConnState set on Srv never ran")
	}
}
