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
	"math/big"
	"net"
	stdhttp "net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/quic-go/quic-go/http3"
)

// writePair writes a self-signed pair for 127.0.0.1/localhost with the given
// common name and returns a pool that trusts it.
func writePair(t *testing.T, certFile, keyFile, cn string) *x509.CertPool {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: cn},
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
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kd}), 0o600); err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return pool
}

// touch moves a file's modification time forward so a rotation is visible even
// when the filesystem has coarse timestamps.
func touch(t *testing.T, path string) {
	t.Helper()
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
}

func cn(c *tls.Certificate) string {
	leaf, err := x509.ParseCertificate(c.Certificate[0])
	if err != nil {
		return err.Error()
	}
	return leaf.Subject.CommonName
}

func TestCertReloaderFollowsItsFiles(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "c.crt"), filepath.Join(dir, "c.key")
	writePair(t, certFile, keyFile, "first")

	var reloadErrs []error
	r, err := newCertReloader(certFile, keyFile, func(err error) { reloadErrs = append(reloadErrs, err) })
	if err != nil {
		t.Fatal(err)
	}
	r.interval = 0 // check on every handshake
	get := func() string {
		c, err := r.GetCertificate(nil)
		if err != nil {
			t.Fatal(err)
		}
		return cn(c)
	}
	if got := get(); got != "first" {
		t.Fatalf("initial certificate = %q", got)
	}
	if got := get(); got != "first" || len(reloadErrs) != 0 {
		t.Fatalf("unchanged files must not reload: %q %v", got, reloadErrs)
	}

	// a rotation is picked up by the next handshake
	writePair(t, certFile, keyFile, "second")
	touch(t, certFile)
	touch(t, keyFile)
	if got := get(); got != "second" {
		t.Fatalf("after rotation = %q, want second", got)
	}

	// a half-written rotation (new certificate, old key) keeps the previous pair
	oldKey, _ := os.ReadFile(keyFile)
	writePair(t, certFile, filepath.Join(dir, "other.key"), "mismatch")
	if err := os.WriteFile(keyFile, oldKey, 0o600); err != nil {
		t.Fatal(err)
	}
	touch(t, certFile)
	if got := get(); got != "second" {
		t.Fatalf("a mismatched pair must be ignored, got %q", got)
	}
	if len(reloadErrs) == 0 {
		t.Fatal("the failed reload must be reported")
	}

	// garbage is ignored too
	if err := os.WriteFile(certFile, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	touch(t, certFile)
	if got := get(); got != "second" {
		t.Fatalf("garbage must be ignored, got %q", got)
	}

	// the interval limits how often the files are looked at
	r.interval = time.Hour
	writePair(t, certFile, keyFile, "third")
	touch(t, certFile)
	touch(t, keyFile)
	r.lastCheck.Store(time.Now().UnixNano())
	if got := get(); got != "second" {
		t.Fatalf("inside the interval nothing is re-read, got %q", got)
	}
	if err := r.Reload(); err != nil || get() != "third" {
		t.Fatalf("an explicit Reload is immediate: %v", err)
	}
}

func TestNewCertReloaderFailsOnABadPair(t *testing.T) {
	dir := t.TempDir()
	if _, err := newCertReloader(filepath.Join(dir, "no.crt"), filepath.Join(dir, "no.key"), nil); err == nil {
		t.Fatal("a missing pair must fail startup")
	}
}

// End to end: Reload (the hook app.Run calls on SIGHUP) makes the running
// server serve a rotated certificate on new connections, over HTTPS and
// HTTP/3, without a restart. The server itself handles no signal.
func TestReloadServesTheRotatedCertificate(t *testing.T) {
	h := start(t, func(c *ServerConfig) { c.HTTP3.Enabled, c.TLS.Enabled = true, true })
	served := func(tcp bool) string {
		t.Helper()
		var cert *x509.Certificate
		if tcp {
			conn, err := tls.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(h.tls)), &tls.Config{InsecureSkipVerify: true}) //nolint:gosec // inspecting the served certificate in a test
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.Close() }()
			cert = conn.ConnectionState().PeerCertificates[0]
		} else {
			tr := &http3.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec // inspecting the served certificate in a test
			defer func() { _ = tr.Close() }()
			resp, err := (&stdhttp.Client{Transport: tr, Timeout: 5 * time.Second}).Get(addr("https", h.h3, "/ping"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			cert = resp.TLS.PeerCertificates[0]
		}
		return cert.Subject.CommonName
	}
	waitUp := func() {
		for range 100 {
			if refused(h.tls) {
				time.Sleep(20 * time.Millisecond)
				continue
			}
			return
		}
	}
	waitUp()
	if got := served(true); got != "nforge-test" {
		t.Fatalf("initial certificate = %q", got)
	}

	writePair(t, h.certFile, h.keyFile, "rotated")
	if err := h.s.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := served(true); got != "rotated" {
		t.Fatalf("HTTPS serves %q after Reload", got)
	}
	if got := served(false); got != "rotated" {
		t.Fatalf("HTTP/3 serves %q, want rotated (it shares the pair)", got)
	}
}
