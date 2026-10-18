package jose

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"
)

func TestShutdownLifecycle(t *testing.T) {
	s, err := New(ctx, testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	token, _ := s.Sign(ctx, "msg-ed", []byte("x"))
	if err := s.Check(ctx); err != nil {
		t.Fatal(err)
	}

	// an admitted operation blocks the drain until released
	if _, err := s.acquire(ctx, "test", ""); err != nil {
		t.Fatal(err)
	}
	short, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	err = s.Shutdown(short)
	mustIs(t, err, context.DeadlineExceeded)

	_, err = s.Verify(ctx, "msg-ed", token)
	mustIs(t, err, ErrClosed)
	mustIs(t, s.Check(ctx), ErrClosed)
	mustIs(t, s.Reload(ctx), ErrClosed)
	_, err = s.PublicJWKS(ctx)
	mustIs(t, err, ErrClosed)

	s.release()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatalf("second shutdown must finish the drain: %v", err)
	}
	if err := s.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown must be idempotent: %v", err)
	}
	if _, ok := s.Profile("msg-ed"); !ok {
		t.Fatal("Profile must keep working after shutdown")
	}
	if k := s.state.Load().keys["hs-sign"]; k.secret[0] != 0 || k.secret[31] != 0 {
		t.Fatal("secrets must be cleared after the drain")
	}
}

func TestReload(t *testing.T) {
	m := testMaterial(t)
	cfg := testConfig(t)
	s := newService(t, cfg, newTestClock().Option())
	token, _ := s.IssueJWT(ctx, "access", RegisteredClaims{Subject: "u"}, nil)
	before := s.state.Load()

	// identical material: no-op
	if err := s.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if s.state.Load() != before {
		t.Fatal("identical reload must keep the snapshot")
	}

	// a broken source keeps the previous valid state
	edPath := cfg.Keys[0].Source.File
	if err := os.WriteFile(edPath, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustIs(t, s.Reload(ctx), ErrInvalidKey)
	if s.state.Load() != before {
		t.Fatal("failed reload replaced the snapshot")
	}
	if _, err := s.ValidateJWT(ctx, "access", token, nil); err != nil {
		t.Fatalf("old state must keep working: %v", err)
	}

	// new material under the same kid: old tokens stop validating
	if err := os.WriteFile(edPath, pkcs8PEM(t, m.ed2), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	_, err := s.ValidateJWT(ctx, "access", token, nil)
	mustIs(t, err, ErrInvalidSignature)
	fresh, err := s.IssueJWT(ctx, "access", RegisteredClaims{Subject: "u"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ValidateJWT(ctx, "access", fresh, nil); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentUseReloadAndShutdown(t *testing.T) {
	s, err := New(ctx, testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for range 8 {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				token, err := s.IssueJWT(ctx, "nested", RegisteredClaims{Subject: "u"}, nil)
				if errors.Is(err, ErrClosed) {
					return
				}
				if err != nil {
					t.Error(err)
					return
				}
				if _, err := s.ValidateJWT(ctx, "nested", token, nil); err != nil && !errors.Is(err, ErrClosed) {
					t.Error(err)
					return
				}
			}
		})
	}
	wg.Go(func() {
		for range 5 {
			if err := s.Reload(ctx); err != nil && !errors.Is(err, ErrClosed) {
				t.Error(err)
			}
		}
	})
	time.Sleep(100 * time.Millisecond)
	if err := s.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	close(stop)
	wg.Wait()
}

func TestIndependentInstances(t *testing.T) {
	a := newService(t, testConfig(t))
	cfg := testConfig(t)
	m := testMaterial(t)
	cfg.Keys[0].Source.File = writeKey(t, t.TempDir(), "other-ed", pkcs8PEM(t, m.ed2))
	cfg.Profiles[7].JWT.Issuer = "https://b.test"
	b := newService(t, cfg)

	token, err := a.IssueJWT(ctx, "access", RegisteredClaims{Subject: "u"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = b.ValidateJWT(ctx, "access", token, nil)
	mustIs(t, err, ErrInvalidSignature)
	if err := a.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Sign(ctx, "msg-ed", nil); err != nil {
		t.Fatalf("shutting down one instance affected another: %v", err)
	}
}
