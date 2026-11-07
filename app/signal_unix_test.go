//go:build unix

package app

import (
	"context"
	"syscall"
	"testing"
	"time"

	"github.com/ams-soft/nforge/module"
)

// SIGHUP reloads, SIGTERM stops: Run owns the process signals, no module does.
func TestRunHandlesSignals(t *testing.T) {
	reloaded := make(chan struct{}, 1)
	m := module.New("certs", module.Definition{Init: func(context.Context, *module.Scope) (module.Hooks, error) {
		return module.Hooks{Reload: func(context.Context) error { reloaded <- struct{}{}; return nil }}, nil
	}})
	a, err := New(load(t, ""), Modules(m))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- a.Run(context.Background()) }()
	waitReady(t, a)
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	select {
	case <-reloaded:
	case <-time.After(5 * time.Second):
		t.Fatal("SIGHUP did not reload")
	}
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SIGTERM did not stop Run")
	}
}
