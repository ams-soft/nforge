//go:build unix

package jose

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A FIFO as key source must be refused, not wait for a writer forever.
func TestKeySourceFIFOIsRefusedWithoutBlocking(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "key.pem")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, _, err := readKeyFile(fifo, 1024)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a FIFO must be refused")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("readKeyFile blocked on a FIFO")
	}
}
