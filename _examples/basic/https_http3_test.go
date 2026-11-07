package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"log/slog"
	"net"
	stdhttp "net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ams-soft/nforge/app"
	"github.com/ams-soft/nforge/config"
	"github.com/quic-go/quic-go/http3"
)

// freePort returns a port that is free for both TCP and UDP.
func freePort(t *testing.T) int {
	t.Helper()
	for range 50 {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := ln.Addr().(*net.TCPAddr).Port
		_ = ln.Close()
		pc, err := net.ListenPacket("udp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			_ = pc.Close()
			return port
		}
	}
	t.Fatal("no free port")
	return 0
}

// TestHTTPSHTTP3Live starts the real example with config.https-http3.yaml and
// the committed local-development certificate, on free ports (an env file
// overrides the ports only), and talks to it over real TCP and UDP.
func TestHTTPSHTTP3Live(t *testing.T) {
	t.Chdir("../..") // the certificate paths in the YAML are relative to the repository root
	httpPort, tlsPort := freePort(t), freePort(t)
	env := filepath.Join(t.TempDir(), "live.env")
	envText := fmt.Sprintf("DATABASE_ENABLED=false\nLOGGING_OUTPUT_TERMINAL_ENABLED=false\nTRACING_OTEL_ENABLED=false\nHTTP_SERVER_HEADER=false\n"+
		"HTTP_SERVER_PORT=%d\nHTTP_SERVER_TLS_PORT=%d\nHTTP_SERVER_HTTP3_PORT=%d\n", httpPort, tlsPort, tlsPort)
	if err := os.WriteFile(env, []byte(envText), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFlags("_examples/basic/config.https-http3.yaml", env)
	if err != nil {
		t.Fatal(err)
	}
	a, err := newApp(cfg, app.WithLogger(slog.New(slog.DiscardHandler)))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Start(context.Background()); err != nil { // returns once every listener is bound
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := a.Shutdown(ctx); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	}()

	// Trust exactly the example certificate: no InsecureSkipVerify anywhere.
	pem, err := os.ReadFile("_examples/basic/certs/localhost.crt")
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		t.Fatal("bad example certificate")
	}
	tlsConf := &tls.Config{RootCAs: pool}
	noRedirect := func(*stdhttp.Request, []*stdhttp.Request) error { return stdhttp.ErrUseLastResponse }
	tcp := &stdhttp.Client{Timeout: 5 * time.Second, CheckRedirect: noRedirect,
		Transport: &stdhttp.Transport{TLSClientConfig: tlsConf, ForceAttemptHTTP2: true}}
	h3 := &stdhttp.Client{Timeout: 5 * time.Second, Transport: &http3.Transport{TLSClientConfig: tlsConf}}
	defer func() { _ = h3.Transport.(*http3.Transport).Close() }()

	httpsURL := fmt.Sprintf("https://127.0.0.1:%d", tlsPort)
	do := func(c *stdhttp.Client, url string) *stdhttp.Response {
		t.Helper()
		var lastErr error
		for range 20 { // the listeners are bound once Start returns; QUIC may need a retry
			r, err := c.Get(url)
			if err == nil {
				return r
			}
			lastErr = err
			select {
			case <-a.Failed():
				t.Fatalf("server stopped: %v", a.Err())
			case <-time.After(50 * time.Millisecond):
			}
		}
		t.Fatalf("GET %s: %v", url, lastErr)
		return nil
	}

	// HTTPS (h2) on an existing route, with the global middleware and Alt-Svc
	r := do(tcp, httpsURL+"/health")
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if r.StatusCode != 200 || r.ProtoMajor != 2 || r.TLS.Version != tls.VersionTLS13 {
		t.Fatalf("https: %d %s tls=%x %s", r.StatusCode, r.Proto, r.TLS.Version, b)
	}
	if r.Header.Get("X-Clacks-Overhead") == "" {
		t.Error("the global middleware did not run over HTTPS")
	}
	if as := r.Header.Get("Alt-Svc"); !strings.Contains(as, fmt.Sprintf(`h3=":%d"`, tlsPort)) {
		t.Errorf("Alt-Svc = %q", as)
	}

	// HTTP/3 on the same route
	r = do(h3, httpsURL+"/health")
	b3, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if r.StatusCode != 200 || r.ProtoMajor != 3 || r.Header.Get("X-Clacks-Overhead") == "" || string(b3) != string(b) {
		t.Fatalf("http3: %d %s clacks=%q %s", r.StatusCode, r.Proto, r.Header.Get("X-Clacks-Overhead"), b3)
	}

	// plain HTTP only redirects, and nothing of the application is served there
	for _, path := range []string{"/health?x=1", "/docs", "/api/v1/users", "/auth/me"} {
		r, err := tcp.Get(fmt.Sprintf("http://127.0.0.1:%d%s", httpPort, path))
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		if want := httpsURL + path; r.StatusCode != 308 || r.Header.Get("Location") != want {
			t.Errorf("%s: %d Location=%q, want 308 %q", path, r.StatusCode, r.Header.Get("Location"), want)
		}
		if r.Header.Get("X-Clacks-Overhead") != "" {
			t.Errorf("%s: the application middleware ran on the redirect listener", path)
		}
	}
}
