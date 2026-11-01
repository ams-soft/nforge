package nhttp

import (
	"crypto/tls"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// certCheckInterval is how often a handshake looks at the certificate files'
// modification times. A handshake is the only trigger, so an idle server does
// no I/O.
const certCheckInterval = 30 * time.Second

// certReloader serves a TLS certificate that follows its files: when the
// certificate or key file changes (ACME, cert-manager, a rotated secret) the
// next handshake after certCheckInterval, or a Reload on SIGHUP, picks the new
// pair up without a restart or a dropped connection. A pair that fails to load
// or does not match (a half-written rotation) is ignored and the previous
// certificate keeps serving.
type certReloader struct {
	certFile, keyFile string
	interval          time.Duration
	onError           func(error)

	cur       atomic.Pointer[tls.Certificate]
	lastCheck atomic.Int64 // unix nanoseconds of the last mtime check

	mu              sync.Mutex // serializes reloads and guards the mtimes
	certMod, keyMod time.Time
}

// newCertReloader loads the pair once; a failure is returned so startup stays
// all-or-nothing.
func newCertReloader(certFile, keyFile string, onError func(error)) (*certReloader, error) {
	r := &certReloader{certFile: certFile, keyFile: keyFile, interval: certCheckInterval, onError: onError}
	if err := r.Reload(); err != nil {
		return nil, err
	}
	r.lastCheck.Store(time.Now().UnixNano())
	return r, nil
}

// Reload loads the pair now. On failure the current certificate is kept.
func (r *certReloader) Reload() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cert, err := tls.LoadX509KeyPair(r.certFile, r.keyFile)
	if err != nil {
		return fmt.Errorf("load tls certificate: %w", err)
	}
	r.cur.Store(&cert)
	r.certMod, r.keyMod = modTime(r.certFile), modTime(r.keyFile)
	return nil
}

func modTime(path string) time.Time {
	if info, err := os.Stat(path); err == nil {
		return info.ModTime()
	}
	return time.Time{}
}

// GetCertificate is tls.Config.GetCertificate.
func (r *certReloader) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	r.maybeReload()
	return r.cur.Load(), nil
}

// maybeReload re-reads the pair when a file changed since it was loaded, at
// most once per interval.
func (r *certReloader) maybeReload() {
	now := time.Now().UnixNano()
	last := r.lastCheck.Load()
	if now-last < int64(r.interval) || !r.lastCheck.CompareAndSwap(last, now) {
		return
	}
	r.mu.Lock()
	changed := !modTime(r.certFile).Equal(r.certMod) || !modTime(r.keyFile).Equal(r.keyMod)
	r.mu.Unlock()
	if !changed {
		return
	}
	if err := r.Reload(); err != nil && r.onError != nil {
		r.onError(err)
	}
}
