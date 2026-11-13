package scheduler

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/ams-soft/nforge/internal/apptest"
	"github.com/ams-soft/nforge/module"
)

type recorder struct {
	mu sync.Mutex
	ev []string
}

func (r *recorder) add(s string) { r.mu.Lock(); r.ev = append(r.ev, s); r.mu.Unlock() }
func (r *recorder) get() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.ev...)
}

// resource stands for the database: the job uses it, so it must close only
// after the running handler returned.
func resource(events *recorder, res module.Capability[*int]) module.Descriptor {
	return module.Descriptor{ID: "resource", Configure: func(module.Source) (module.Definition, error) {
		return module.Definition{Provides: []module.Handle{res}, Init: func(_ context.Context, sc *module.Scope) (module.Hooks, error) {
			module.Provide(sc, res, new(int))
			return module.Hooks{Stop: func(context.Context) error { events.add("resource closed"); return nil }}, nil
		}}, nil
	}}
}

// The scheduler is an ingress module: on shutdown it stops first, cancels the
// handler that is running and waits for it, and only then do the modules the
// handler depends on close. It also starts after every Init.
func TestShutdownCancelsAndWaitsForHandlersBeforeResourcesClose(t *testing.T) {
	fc := newFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	old := moduleClock
	moduleClock = func() clock { return fc }
	t.Cleanup(func() { moduleClock = old })

	events := &recorder{}
	res := module.NewCapability[*int]("resource")
	entered := make(chan struct{})
	stock := module.New("stock", module.Definition{
		Requires: []module.Handle{Capability, res},
		Init: func(_ context.Context, sc *module.Scope) (module.Hooks, error) {
			_ = module.Require(sc, res)
			return module.Hooks{}, module.Require(sc, Capability).AddJob("* * * * *", func(ctx context.Context) error {
				close(entered)
				<-ctx.Done()
				events.add("handler canceled")
				time.Sleep(50 * time.Millisecond) // finishing takes a while
				events.add("handler done")
				return nil
			})
		},
	})
	a := apptest.Start(t, "", resource(events, res), Module(), stock)
	fc.step(t)
	recv(t, entered)
	if err := a.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"handler canceled", "handler done", "resource closed"}
	got := events.get()
	if len(got) != len(want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("events = %v, want %v", got, want)
		}
	}
}
