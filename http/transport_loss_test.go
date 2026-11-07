package nhttp

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	stdhttp "net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quic-go/quic-go/http3"
)

// lossyUDP forwards datagrams between one client and target, dropping a fraction
// of them in both directions.
type lossyUDP struct {
	front  *net.UDPConn
	back   *net.UDPConn
	client atomic.Pointer[net.UDPAddr]
	drops  atomic.Int64
	sent   atomic.Int64
	mu     sync.Mutex
	random *rand.Rand
	rate   float64
}

func newLossyUDP(t *testing.T, target int, rate float64) *lossyUDP {
	t.Helper()
	front, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	back, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: target})
	if err != nil {
		t.Fatal(err)
	}
	p := &lossyUDP{front: front, back: back, rate: rate, random: rand.New(rand.NewPCG(1, 2))} //nolint:gosec // reproducible test loss
	t.Cleanup(func() { _ = front.Close(); _ = back.Close() })
	go p.pump(func(b []byte) (int, error) {
		n, from, err := front.ReadFromUDP(b)
		if err == nil {
			p.client.Store(from)
		}
		return n, err
	}, func(b []byte) { _, _ = back.Write(b) })
	go p.pump(back.Read, func(b []byte) {
		if c := p.client.Load(); c != nil {
			_, _ = front.WriteToUDP(b, c)
		}
	})
	return p
}

func (p *lossyUDP) drop() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.random.Float64() < p.rate
}

func (p *lossyUDP) pump(read func([]byte) (int, error), write func([]byte)) {
	buf := make([]byte, 65535)
	for {
		n, err := read(buf)
		if err != nil {
			return
		}
		if p.drop() {
			p.drops.Add(1)
			continue
		}
		p.sent.Add(1)
		write(buf[:n])
	}
}

// QUIC retransmits over a lossy path: requests and a large body arrive intact,
// and the shutdown afterwards stays bounded.
func TestHTTP3SurvivesPacketLoss(t *testing.T) {
	h := start(t, func(c *ServerConfig) { c.HTTP3.Enabled = true })
	direct := h.h3Client()
	get(t, direct, addr("https", h.h3, "/ping")).Body.Close() // the listener is up

	proxy := newLossyUDP(t, h.h3, 0.1)
	port := proxy.front.LocalAddr().(*net.UDPAddr).Port
	transport := &http3.Transport{TLSClientConfig: &tls.Config{RootCAs: h.pool}}
	t.Cleanup(func() { _ = transport.Close() })
	client := &stdhttp.Client{Timeout: 30 * time.Second, Transport: transport}
	url := "https://127.0.0.1:" + strconv.Itoa(port)

	payload := bytes.Repeat([]byte("nforge-quic-"), 32<<10) // 384 KiB: many packets
	for range 5 {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		req, err := stdhttp.NewRequestWithContext(ctx, stdhttp.MethodPost, url+"/echo", bytes.NewReader(payload))
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		resp, err := client.Do(req)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		got, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		cancel()
		if err != nil || !bytes.Equal(got, payload) {
			t.Fatalf("body: %d of %d bytes, err %v", len(got), len(payload), err)
		}
	}
	if proxy.drops.Load() == 0 {
		t.Fatal("the proxy dropped nothing: the test proved nothing")
	}
	t.Logf("dropped %d of %d datagrams", proxy.drops.Load(), proxy.drops.Load()+proxy.sent.Load())

	// A lost GOAWAY can keep the drain waiting for a client that will not answer:
	// the outcome is then the drain deadline and a forced close, bounded, as with
	// HTTP/2 in TestConcurrentTrafficAndShutdown. Anything else is a failure.
	began := time.Now()
	if err := h.stop(t); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("shutdown: %v", err)
	}
	if took := time.Since(began); took > shutdownTimeout+2*time.Second {
		t.Errorf("shutdown took %s", took)
	}
}
