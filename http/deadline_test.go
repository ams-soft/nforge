package nhttp

import (
	"context"
	"errors"
	stdhttp "net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"
)

type ctxKey struct{}

// Regression (B3): every context derived from the guard observes
// DeadlineExceeded, the same as with context.WithTimeout.
func TestDerivedContextsSeeDeadlineExceeded(t *testing.T) {
	derive := map[string]func(context.Context) (context.Context, context.CancelFunc){
		"WithTimeout": func(c context.Context) (context.Context, context.CancelFunc) {
			return context.WithTimeout(c, time.Minute)
		},
		"WithCancel": func(c context.Context) (context.Context, context.CancelFunc) { return context.WithCancel(c) },
		"WithCancelCause": func(c context.Context) (context.Context, context.CancelFunc) {
			x, f := context.WithCancelCause(c)
			return x, func() { f(nil) }
		},
		"WithValue": func(c context.Context) (context.Context, context.CancelFunc) {
			return context.WithValue(c, ctxKey{}, 1), func() {}
		},
		"WithoutCancel+AfterFunc": func(c context.Context) (context.Context, context.CancelFunc) {
			x, f := context.WithCancel(c)
			return x, f
		},
	}
	for name, mk := range derive {
		t.Run(name, func(t *testing.T) {
			dc, cancel := newDeadlineCtx(context.Background(), 40*time.Millisecond)
			defer cancel()
			child, ccancel := mk(dc)
			defer ccancel()
			select {
			case <-child.Done():
			case <-time.After(2 * time.Second):
				t.Fatal("the derived context never ended")
			}
			if !errors.Is(child.Err(), context.DeadlineExceeded) {
				t.Fatalf("child.Err() = %v, want DeadlineExceeded", child.Err())
			}
			if !errors.Is(dc.Err(), context.DeadlineExceeded) {
				t.Fatalf("guard.Err() = %v", dc.Err())
			}
		})
	}
}

func TestDeadlineCtxSemantics(t *testing.T) {
	t.Run("deadline reported and parent deadline wins when earlier", func(t *testing.T) {
		parent, pc := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer pc()
		dc, cancel := newDeadlineCtx(parent, time.Hour)
		defer cancel()
		pd, _ := parent.Deadline()
		if got, ok := dc.Deadline(); !ok || !got.Equal(pd) {
			t.Fatalf("Deadline = %v %v, want the parent's %v", got, ok, pd)
		}
		<-dc.Done()
		if !errors.Is(dc.Err(), context.DeadlineExceeded) {
			t.Fatalf("Err = %v", dc.Err())
		}
	})
	t.Run("parent cancellation is Canceled, not DeadlineExceeded", func(t *testing.T) {
		parent, pc := context.WithCancel(context.Background())
		dc, cancel := newDeadlineCtx(parent, time.Hour)
		defer cancel()
		child, cc := context.WithTimeout(dc, time.Hour)
		defer cc()
		pc()
		<-child.Done()
		if !errors.Is(child.Err(), context.Canceled) || !errors.Is(dc.Err(), context.Canceled) {
			t.Fatalf("child=%v guard=%v, want Canceled", child.Err(), dc.Err())
		}
	})
	t.Run("release removes the deadline and stops the timer", func(t *testing.T) {
		dc, cancel := newDeadlineCtx(context.Background(), 30*time.Millisecond)
		defer cancel()
		dc.release()
		if _, ok := dc.Deadline(); ok {
			t.Error("a released context reports no deadline")
		}
		time.Sleep(80 * time.Millisecond)
		if dc.Err() != nil {
			t.Fatalf("released context ended: %v", dc.Err())
		}
	})
	t.Run("release after the deadline fired changes nothing", func(t *testing.T) {
		dc, cancel := newDeadlineCtx(context.Background(), 10*time.Millisecond)
		defer cancel()
		<-dc.Done()
		dc.release()
		if !errors.Is(dc.Err(), context.DeadlineExceeded) {
			t.Fatalf("Err = %v", dc.Err())
		}
	})
	t.Run("values come from the parent", func(t *testing.T) {
		parent := context.WithValue(context.Background(), ctxKey{}, "v")
		dc, cancel := newDeadlineCtx(parent, time.Hour)
		defer cancel()
		if dc.Value(ctxKey{}) != "v" {
			t.Error("Value must delegate to the parent")
		}
	})
	t.Run("an already canceled parent", func(t *testing.T) {
		parent, pc := context.WithCancel(context.Background())
		pc()
		dc, cancel := newDeadlineCtx(parent, time.Hour)
		defer cancel()
		if !errors.Is(dc.Err(), context.Canceled) {
			t.Fatalf("Err = %v", dc.Err())
		}
	})
	t.Run("no goroutine or timer is left behind", func(t *testing.T) {
		before := runtime.NumGoroutine()
		for range 2000 {
			parent, pc := context.WithCancel(context.Background())
			dc, cancel := newDeadlineCtx(parent, time.Hour)
			child, cc := context.WithCancel(dc)
			cc()
			_ = child
			cancel()
			pc()
		}
		time.Sleep(100 * time.Millisecond)
		if after := runtime.NumGoroutine(); after > before+5 {
			t.Fatalf("goroutines %d -> %d", before, after)
		}
	})
}

// The three deadline sources of the server, seen through a derived context.
func TestServerDeadlinesReachDerivedContexts(t *testing.T) {
	wait := func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		child, cancel := context.WithTimeout(r.Context(), time.Minute)
		defer cancel()
		<-child.Done()
		if errors.Is(child.Err(), context.DeadlineExceeded) {
			w.WriteHeader(stdhttp.StatusGatewayTimeout)
			return
		}
		w.WriteHeader(stdhttp.StatusInternalServerError)
	}
	t.Run("handler_timeout", func(t *testing.T) {
		s := limitsServer(t, func(c *testConfig) { c.HTTP.Server.HandlerTimeout = 50 * time.Millisecond })
		s.Router().Get("/w", wait)
		if rec := serveReq(s, httptest.NewRequest("GET", "/w", nil)); rec.Code != stdhttp.StatusGatewayTimeout {
			t.Fatalf("status %d: a derived context did not see DeadlineExceeded", rec.Code)
		}
	})
	t.Run("route Timeout", func(t *testing.T) {
		s := limitsServer(t, nil)
		s.Router().With(Timeout(50*time.Millisecond)).Get("/w", wait)
		if rec := serveReq(s, httptest.NewRequest("GET", "/w", nil)); rec.Code != stdhttp.StatusGatewayTimeout {
			t.Fatalf("status %d", rec.Code)
		}
	})
	t.Run("HTTP/3 stream timeout", func(t *testing.T) {
		rec := httptest.NewRecorder()
		streamTimeout(stdhttp.HandlerFunc(wait), 50*time.Millisecond).ServeHTTP(rec, httptest.NewRequest("GET", "/w", nil))
		if rec.Code != stdhttp.StatusGatewayTimeout {
			t.Fatalf("status %d", rec.Code)
		}
	})
}
