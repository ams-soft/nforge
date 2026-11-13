package email

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeServer is a scripted in-process SMTP server with real TLS.
type fakeServer struct {
	t        *testing.T
	ln       net.Listener
	cert     tls.Certificate
	pool     *x509.CertPool
	implicit bool // TLS from the first byte
	noStart  bool // does not advertise STARTTLS
	auth     bool // advertises AUTH PLAIN (only after TLS)

	rcptReply     string // e.g. "550 no such user"; "" = 250
	dataReply     string // reply to the final "." ; "" = 250
	hangAfterData bool   // read the body, never answer
	dropAfterData bool   // close without answering

	mu          sync.Mutex
	messages    []string
	authLine    string
	authInClear bool
	conns       int
	wg          sync.WaitGroup
}

func newCert(t *testing.T, host string) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: host},
		DNSNames: []string{host}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA: true, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool
}

func startServer(t *testing.T, mod func(*fakeServer)) *fakeServer {
	t.Helper()
	s := &fakeServer{t: t}
	s.cert, s.pool = newCert(t, "mail.test")
	if mod != nil {
		mod(s)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s.ln = ln
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			s.mu.Lock()
			s.conns++
			s.mu.Unlock()
			s.wg.Add(1)
			go func() { defer s.wg.Done(); s.serve(c) }()
		}
	}()
	t.Cleanup(func() { ln.Close(); s.wg.Wait() })
	return s
}

func (s *fakeServer) addr() (string, int) {
	h, p, _ := net.SplitHostPort(s.ln.Addr().String())
	var port int
	for _, ch := range p {
		port = port*10 + int(ch-'0')
	}
	return h, port
}

// driver returns an smtp driver pointed at the server, trusting its certificate.
func (s *fakeServer) driver(security, user, pass string) *smtpDriver {
	_, port := s.addr()
	return &smtpDriver{
		cfg:       SMTPConfig{Host: "mail.test", Port: port, Security: security, Username: user, Password: pass},
		tlsConfig: &tls.Config{RootCAs: s.pool},
		dial: func(ctx_ context.Context, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx_, "tcp", s.ln.Addr().String())
		},
	}
}

func (s *fakeServer) serve(raw net.Conn) {
	defer raw.Close()
	conn := raw
	secure := false
	if s.implicit {
		tc := tls.Server(raw, &tls.Config{Certificates: []tls.Certificate{s.cert}})
		if tc.Handshake() != nil {
			return
		}
		conn, secure = tc, true
	}
	r := bufio.NewReader(conn)
	w := func(l string) { _, _ = conn.Write([]byte(l + "\r\n")) }
	w("220 mail.test ready")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		up := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(up, "EHLO"):
			w("250-mail.test")
			if !s.noStart && !secure {
				w("250-STARTTLS")
			}
			if s.auth {
				w("250-AUTH PLAIN")
			}
			w("250 8BITMIME")
		case up == "STARTTLS":
			w("220 go ahead")
			tc := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{s.cert}})
			if tc.Handshake() != nil {
				return
			}
			conn, secure = tc, true
			r = bufio.NewReader(conn)
		case strings.HasPrefix(up, "AUTH PLAIN"):
			s.mu.Lock()
			s.authLine = line
			s.authInClear = !secure
			s.mu.Unlock()
			w("235 ok")
		case strings.HasPrefix(up, "MAIL FROM:"):
			w("250 ok")
		case strings.HasPrefix(up, "RCPT TO:"):
			if s.rcptReply != "" {
				w(s.rcptReply)
			} else {
				w("250 ok")
			}
		case up == "DATA":
			w("354 go")
			var body strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				body.WriteString(strings.TrimPrefix(l, ".")) // undo dot-stuffing: only a leading dot
			}
			if s.hangAfterData {
				_, _ = io.Copy(io.Discard, r) // hold the connection until the client gives up
				return
			}
			if s.dropAfterData {
				return
			}
			if s.dataReply != "" {
				w(s.dataReply)
				continue
			}
			s.mu.Lock()
			s.messages = append(s.messages, body.String())
			s.mu.Unlock()
			w("250 queued")
		case up == "QUIT":
			w("221 bye")
			return
		default:
			w("502 unexpected")
		}
	}
}

func (s *fakeServer) got() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.messages...)
}
