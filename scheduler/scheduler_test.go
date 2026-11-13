package scheduler

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ams-soft/nforge/metrics"
)

// fakeClock is a manual clock: timers fire only when the test advances time.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	c        chan time.Time
	deadline time.Time
	done     bool
}

func (t *fakeTimer) C() <-chan time.Time { return t.c }
func (t *fakeTimer) Stop() bool          { return true }

func newFakeClock(now time.Time) *fakeClock { return &fakeClock{now: now} }

func (f *fakeClock) Now() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.now }

func (f *fakeClock) NewTimer(d time.Duration) timer {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := &fakeTimer{c: make(chan time.Time, 1), deadline: f.now.Add(d)}
	f.timers = append(f.timers, t)
	return t
}

// step waits for the loop to arm a timer, advances the clock exactly to it and
// fires it. It returns the new time.
func (f *fakeClock) step(t testing.TB) time.Time {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		f.mu.Lock()
		for _, tm := range f.timers {
			if !tm.done {
				tm.done = true
				if tm.deadline.After(f.now) {
					f.now = tm.deadline
				}
				now := f.now
				tm.c <- now
				f.mu.Unlock()
				return now
			}
		}
		f.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("the scheduler never armed a timer")
		}
		runtime.Gosched()
		time.Sleep(time.Millisecond)
	}
}

// idle waits until the loop dispatched the firing it was woken for and armed
// its next timer, so a test can rely on the firing having been decided.
func (f *fakeClock) idle(t testing.TB) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		f.mu.Lock()
		for _, tm := range f.timers {
			if !tm.done {
				f.mu.Unlock()
				return
			}
		}
		f.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("the scheduler never armed its next timer")
		}
		time.Sleep(time.Millisecond)
	}
}

// jump moves the clock without firing a timer (a suspended process).
func (f *fakeClock) jump(d time.Duration) { f.mu.Lock(); f.now = f.now.Add(d); f.mu.Unlock() }

func testScheduler(t testing.TB, cfg Config, zone string, start time.Time) (*Scheduler, *fakeClock, *syncBuffer) {
	t.Helper()
	cfg.ApplyDefaults()
	cfg.Timezone = zone
	loc, err := cfg.Validate()
	if err != nil {
		t.Fatal(err)
	}
	var logs syncBuffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	fc := newFakeClock(start)
	s := newScheduler(cfg, loc, logger, fc)
	t.Cleanup(func() { _ = s.stop(context.Background()) })
	return s, fc, &logs
}

type syncBuffer struct {
	mu sync.Mutex
	bytes.Buffer
}

func (b *syncBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.Buffer.String() }

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Write(p)
}

func recv[T any](t testing.TB, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the handler")
		panic("unreachable")
	}
}

func none[T any](t testing.TB, ch <-chan T) {
	t.Helper()
	select {
	case <-ch:
		t.Fatal("unexpected execution")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestFiresAtTheRightInstantInTheConfiguredZone(t *testing.T) {
	start := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC) // 06:00 in Sao Paulo
	s, fc, _ := testScheduler(t, Config{}, "America/Sao_Paulo", start)
	fired := make(chan time.Time, 4)
	if err := s.AddJob("0 7 * * *", func(context.Context) error { fired <- fc.Now(); return nil }); err != nil {
		t.Fatal(err)
	}
	if err := s.start(context.Background()); err != nil {
		t.Fatal(err)
	}
	fc.step(t)
	if got, want := recv(t, fired), time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("fired at %v, want %v (07:00 America/Sao_Paulo)", got, want)
	}
	fc.step(t)
	if got, want := recv(t, fired), time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("second firing at %v, want %v", got, want)
	}
}

func TestIndependentJobs(t *testing.T) {
	s, fc, _ := testScheduler(t, Config{}, "UTC", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	a, b := make(chan string, 8), make(chan string, 8)
	_ = s.AddJob("*/10 * * * *", func(context.Context) error { a <- "a"; return nil })
	_ = s.AddJob("*/30 * * * *", func(context.Context) error { b <- "b"; return nil })
	if err := s.start(context.Background()); err != nil {
		t.Fatal(err)
	}
	for range 3 { // 00:10, 00:20, 00:30
		fc.step(t)
	}
	for range 3 {
		recv(t, a)
	}
	recv(t, b)
	none(t, b)
	none(t, a)
}

func TestOverlappingExecutionsAreParallelByDefault(t *testing.T) {
	s, fc, _ := testScheduler(t, Config{}, "UTC", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	var running, peak atomic.Int32
	release := make(chan struct{})
	entered := make(chan struct{}, 4)
	_ = s.AddJob("* * * * *", func(context.Context) error {
		n := running.Add(1)
		for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
		}
		entered <- struct{}{}
		<-release
		running.Add(-1)
		return nil
	})
	if err := s.start(context.Background()); err != nil {
		t.Fatal(err)
	}
	fc.step(t)
	recv(t, entered)
	fc.step(t) // the first run is still in progress
	recv(t, entered)
	if peak.Load() != 2 {
		t.Fatalf("peak = %d, want 2 overlapping executions", peak.Load())
	}
	close(release)
}

func TestConcurrencyLimitSkipsWithoutQueueOrReplay(t *testing.T) {
	s, fc, logs := testScheduler(t, Config{MaxConcurrency: 1}, "UTC", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	var runs atomic.Int32
	release := make(chan struct{})
	entered := make(chan struct{}, 8)
	_ = s.AddJob("* * * * *", func(context.Context) error {
		runs.Add(1)
		entered <- struct{}{}
		<-release
		return nil
	})
	if err := s.start(context.Background()); err != nil {
		t.Fatal(err)
	}
	fc.step(t) // 00:01 runs
	recv(t, entered)
	fc.step(t) // 00:02 skipped
	fc.step(t) // 00:03 skipped
	fc.idle(t)
	none(t, entered)
	close(release) // the run ends: nothing is replayed for the skipped firings
	none(t, entered)
	if runs.Load() != 1 {
		t.Fatalf("runs = %d, want 1: no queue, no backlog", runs.Load())
	}
	fc.step(t) // 00:04: free again, runs normally
	recv(t, entered)
	if runs.Load() != 2 {
		t.Fatalf("runs = %d, want 2", runs.Load())
	}
	if n := bytes.Count([]byte(logs.String()), []byte("Firing skipped")); n != 2 {
		t.Fatalf("%d skip logs, want 2\n%s", n, logs.String())
	}
}

func TestPerJobLimitDoesNotBlockOtherJobs(t *testing.T) {
	s, fc, _ := testScheduler(t, Config{}, "UTC", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	release := make(chan struct{})
	slow, fast := make(chan struct{}, 8), make(chan struct{}, 8)
	_ = s.AddJob("* * * * *", func(context.Context) error { slow <- struct{}{}; <-release; return nil }, WithMaxConcurrency(1))
	_ = s.AddJob("* * * * *", func(context.Context) error { fast <- struct{}{}; return nil })
	if err := s.start(context.Background()); err != nil {
		t.Fatal(err)
	}
	fc.step(t)
	recv(t, slow)
	recv(t, fast)
	fc.step(t)
	fc.idle(t)
	recv(t, fast) // the saturated slow job is skipped, the other one still runs
	none(t, slow)
	close(release)
}

func TestSuspendedProcessDoesNotReplayMissedFirings(t *testing.T) {
	s, fc, _ := testScheduler(t, Config{}, "UTC", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fired := make(chan time.Time, 16)
	_ = s.AddJob("* * * * *", func(context.Context) error { fired <- fc.Now(); return nil })
	if err := s.start(context.Background()); err != nil {
		t.Fatal(err)
	}
	fc.jump(2 * time.Hour) // 120 firings were missed
	fc.step(t)
	recv(t, fired)
	fc.idle(t)
	none(t, fired)
}

func TestHandlerErrorAndPanicDoNotStopTheSchedule(t *testing.T) {
	s, fc, logs := testScheduler(t, Config{}, "UTC", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	var n atomic.Int32
	done := make(chan struct{}, 8)
	_ = s.AddJob("* * * * *", func(context.Context) error {
		defer func() { done <- struct{}{} }()
		switch n.Add(1) {
		case 1:
			return errors.New("boom")
		case 2:
			panic("kaboom")
		}
		return nil
	})
	if err := s.start(context.Background()); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		fc.step(t)
		recv(t, done)
	}
	if n.Load() != 3 {
		t.Fatalf("runs = %d, want 3", n.Load())
	}
	// let the deferred logging of the last run finish
	if err := s.stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	out := logs.String()
	for _, want := range []string{"Job failed", "error=boom", "Job panicked", "panic=kaboom", "job=job-1"} {
		if !bytes.Contains([]byte(out), []byte(want)) {
			t.Errorf("log lacks %q:\n%s", want, out)
		}
	}
}

func TestEachExecutionHasItsOwnContextCanceledByStop(t *testing.T) {
	s, fc, _ := testScheduler(t, Config{}, "UTC", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	ctxs := make(chan context.Context, 4)
	ended := make(chan error, 4)
	_ = s.AddJob("* * * * *", func(ctx context.Context) error {
		ctxs <- ctx
		<-ctx.Done()
		ended <- ctx.Err()
		return ctx.Err()
	})
	if err := s.start(context.Background()); err != nil {
		t.Fatal(err)
	}
	fc.step(t)
	c1 := recv(t, ctxs)
	fc.step(t)
	c2 := recv(t, ctxs)
	if c1 == c2 {
		t.Fatal("two firings share one context")
	}
	if c1.Err() != nil || c2.Err() != nil {
		t.Fatal("a context ended before Stop")
	}
	if err := s.stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := recv(t, ended); !errors.Is(err, context.Canceled) {
			t.Fatalf("ctx.Err() = %v", err)
		}
	}
}

func TestStopWaitsForHandlersAndHonorsTheDeadline(t *testing.T) {
	s, fc, _ := testScheduler(t, Config{}, "UTC", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	finished := make(chan struct{})
	_ = s.AddJob("* * * * *", func(context.Context) error { // ignores its context
		entered <- struct{}{}
		<-release
		close(finished)
		return nil
	})
	if err := s.start(context.Background()); err != nil {
		t.Fatal(err)
	}
	fc.step(t)
	recv(t, entered)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := s.stop(ctx)
	if !errors.Is(err, ErrStopTimeout) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stop = %v, want ErrStopTimeout wrapping the deadline", err)
	}
	// Stop is idempotent: later calls report the same result and do not hang.
	if err2 := s.stop(context.Background()); !errors.Is(err2, ErrStopTimeout) {
		t.Fatalf("second stop = %v", err2)
	}
	close(release)
	recv(t, finished)
}

func TestStopWithoutStartAndRepeatedLifecycle(t *testing.T) {
	s, _, _ := testScheduler(t, Config{}, "UTC", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if err := s.stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.start(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("start after stop = %v, want ErrClosed", err)
	}
	s2, _, _ := testScheduler(t, Config{}, "UTC", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if err := s2.start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s2.start(context.Background()); !errors.Is(err, ErrStarted) {
		t.Fatalf("second start = %v, want ErrStarted", err)
	}
	if err := s2.ready(context.Background()); err != nil {
		t.Fatalf("ready while running: %v", err)
	}
	if err := s2.stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s2.ready(context.Background()); err == nil {
		t.Fatal("ready after stop")
	}
}

func TestAddJobValidation(t *testing.T) {
	s, _, _ := testScheduler(t, Config{}, "UTC", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	ok := func(context.Context) error { return nil }
	if err := s.AddJob("not cron", ok); !errors.Is(err, ErrInvalidSchedule) {
		t.Fatalf("invalid = %v", err)
	}
	if err := s.AddJob("* * * * * *", ok); !errors.Is(err, ErrInvalidSchedule) {
		t.Fatalf("six fields = %v", err)
	}
	if err := s.AddJob("* * * * *", nil); !errors.Is(err, ErrNilHandler) {
		t.Fatalf("nil handler = %v", err)
	}
	if err := s.AddJob("0 0 31 2 *", ok); !errors.Is(err, ErrNeverFires) {
		t.Fatalf("never fires = %v", err)
	}
	if err := s.AddJob("* * * * *", ok, WithMaxConcurrency(-1)); err == nil {
		t.Fatal("negative cap accepted")
	}
	if len(s.jobs) != 0 {
		t.Fatalf("%d jobs registered by failing calls", len(s.jobs))
	}
	if err := s.AddJob("0 7 * * *", ok); err != nil {
		t.Fatal(err)
	}
	if err := s.start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.AddJob("0 8 * * *", ok); !errors.Is(err, ErrStarted) {
		t.Fatalf("after start = %v, want ErrStarted", err)
	}
	if err := s.stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.AddJob("0 8 * * *", ok); !errors.Is(err, ErrClosed) {
		t.Fatalf("after stop = %v, want ErrClosed", err)
	}
}

func TestJobIDsAreUnique(t *testing.T) {
	s, _, _ := testScheduler(t, Config{}, "UTC", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	for range 3 {
		_ = s.AddJob("0 7 * * *", func(context.Context) error { return nil }) // same expression
	}
	seen := map[string]bool{}
	for _, j := range s.jobs {
		if seen[j.id] {
			t.Fatalf("duplicate id %q", j.id)
		}
		seen[j.id] = true
	}
	if len(seen) != 3 {
		t.Fatalf("%d ids", len(seen))
	}
}

func TestConcurrentAddJobIsRaceFree(t *testing.T) {
	s, _, _ := testScheduler(t, Config{}, "UTC", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() { _ = s.AddJob("* * * * *", func(context.Context) error { return nil }) })
	}
	wg.Wait()
	if len(s.jobs) != 16 {
		t.Fatalf("%d jobs, want 16", len(s.jobs))
	}
}

func TestMetrics(t *testing.T) {
	s, fc, _ := testScheduler(t, Config{MaxConcurrency: 1}, "UTC", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	reg := metrics.New()
	s.registerMetrics(reg)
	release := make(chan struct{})
	entered := make(chan struct{}, 2)
	_ = s.AddJob("* * * * *", func(context.Context) error { entered <- struct{}{}; <-release; return nil })
	if err := s.start(context.Background()); err != nil {
		t.Fatal(err)
	}
	fc.step(t)
	recv(t, entered)
	fc.step(t) // skipped
	fc.idle(t)
	close(release)
	if err := s.stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := reg.Render(&out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`nforge_scheduler_runs_total{job="job-1",outcome="ok"} 1`, `nforge_scheduler_runs_total{job="job-1",outcome="skipped"} 1`, `nforge_scheduler_job_duration_seconds_count{job="job-1"} 1`} {
		if !bytes.Contains(out.Bytes(), []byte(want)) {
			t.Errorf("metrics lack %q:\n%s", want, out.String())
		}
	}
}

func TestNoGoroutineLeakAfterStop(t *testing.T) {
	settle := func() int {
		var n int
		for range 20 {
			runtime.GC()
			n = runtime.NumGoroutine()
			time.Sleep(10 * time.Millisecond)
			if runtime.NumGoroutine() == n {
				break
			}
		}
		return n
	}
	before := settle()
	for range 5 {
		s, fc, _ := testScheduler(t, Config{}, "UTC", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
		entered := make(chan struct{}, 1)
		_ = s.AddJob("* * * * *", func(ctx context.Context) error { entered <- struct{}{}; <-ctx.Done(); return nil })
		if err := s.start(context.Background()); err != nil {
			t.Fatal(err)
		}
		fc.step(t)
		recv(t, entered)
		if err := s.stop(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if after := settle(); after > before {
		t.Fatalf("goroutines: %d before, %d after", before, after)
	}
}

func TestConfigValidate(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  Config
		want error
	}{
		{"distributed", Config{Mode: "distributed"}, ErrUnsupportedMode},
	} {
		tc.cfg.ApplyDefaults()
		if _, err := tc.cfg.Validate(); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
	for name, cfg := range map[string]Config{
		"bad mode":      {Mode: "cluster"},
		"bad zone":      {Timezone: "Mars/Olympus"},
		"local zone":    {Timezone: "Local"},
		"negative cap":  {MaxConcurrency: -1},
		"zone with gap": {Timezone: "America/Sao Paulo"},
	} {
		cfg.ApplyDefaults()
		if _, err := cfg.Validate(); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	var def Config
	def.ApplyDefaults()
	loc, err := def.Validate()
	if err != nil || def.Mode != ModeLocal || loc.String() != "UTC" || def.MaxConcurrency != 0 {
		t.Fatalf("defaults = %+v, %v, %v", def, loc, err)
	}
}

func TestCancellationByStopIsNotAFailure(t *testing.T) {
	s, fc, logs := testScheduler(t, Config{}, "UTC", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	entered := make(chan struct{}, 1)
	_ = s.AddJob("* * * * *", func(ctx context.Context) error { entered <- struct{}{}; <-ctx.Done(); return ctx.Err() })
	if err := s.start(context.Background()); err != nil {
		t.Fatal(err)
	}
	fc.step(t)
	recv(t, entered)
	if err := s.stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	out := logs.String()
	if !bytes.Contains([]byte(out), []byte("canceled by shutdown")) || bytes.Contains([]byte(out), []byte("Job failed")) {
		t.Fatalf("log:\n%s", out)
	}
}
