package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ams-soft/nforge/metrics"
)

// Errors a caller can branch on with errors.Is.
var (
	// ErrStarted is returned by AddJob once the scheduler started: jobs are
	// registered in Init, before anything runs.
	ErrStarted = errors.New("scheduler: jobs cannot be added after start")
	// ErrClosed is returned by AddJob after Stop.
	ErrClosed = errors.New("scheduler: closed")
	// ErrNilHandler is returned by AddJob for a nil handler.
	ErrNilHandler = errors.New("scheduler: handler is required")
	// ErrNeverFires is returned by AddJob for an expression that matches no
	// date in the coming years, such as "0 0 31 2 *".
	ErrNeverFires = errors.New("scheduler: expression never fires")
	// ErrStopTimeout is returned by Stop when handlers were still running at the deadline.
	ErrStopTimeout = errors.New("scheduler: handlers still running at the shutdown deadline")
)

// Handler is the work of a job. Its context is private to the execution and is
// canceled when the scheduler stops, so a handler must honor it. The handler
// decides the period it covers and reads the current time itself: the planned
// firing time is deliberately not an argument.
type Handler func(ctx context.Context) error

// JobOption changes one job.
type JobOption func(*jobOptions) error

type jobOptions struct{ maxConcurrency *int }

// WithMaxConcurrency caps the executions of this job running at once,
// overriding scheduler.max_concurrency. 0 means unlimited; at the cap a firing
// is skipped, never queued.
func WithMaxConcurrency(n int) JobOption {
	return func(o *jobOptions) error {
		if n < 0 {
			return fmt.Errorf("scheduler: max concurrency must not be negative, got %d", n)
		}
		o.maxConcurrency = &n
		return nil
	}
}

type job struct {
	id      string
	expr    string
	sched   schedule
	handler Handler
	max     int // 0 = unlimited
	running atomic.Int32
	next    time.Time // owned by the loop once started
}

// Scheduler runs registered jobs on their Cron schedules. It is safe for
// concurrent use. Obtain it from scheduler.Capability.
type Scheduler struct {
	loc    *time.Location
	max    int
	logger *slog.Logger
	clock  clock

	runs     *metrics.CounterVec
	duration *metrics.HistogramVec

	mu      sync.Mutex
	jobs    []*job
	started bool
	closed  bool

	runCtx    context.Context
	runCancel context.CancelFunc
	stopCh    chan struct{}
	loopDone  chan struct{}
	wg        sync.WaitGroup // executions in flight

	stopOnce sync.Once
	stopped  chan struct{} // closed when the first Stop finished
	stopErr  error
}

func newScheduler(cfg Config, loc *time.Location, logger *slog.Logger, clk clock) *Scheduler {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Scheduler{
		loc: loc, max: cfg.MaxConcurrency, logger: logger, clock: clk,
		stopCh: make(chan struct{}), loopDone: make(chan struct{}), stopped: make(chan struct{}),
	}
}

// Location is the time zone the expressions are evaluated in.
func (s *Scheduler) Location() *time.Location { return s.loc }

// AddJob registers handler to run on the five-field Cron expression frequency
// (for example "0 7 * * *"). It fails, with nothing registered, for an invalid
// expression, a nil handler, a negative cap, or after the scheduler started or
// stopped. Register jobs in the module's Init.
func (s *Scheduler) AddJob(frequency string, handler Handler, opts ...JobOption) error {
	if handler == nil {
		return ErrNilHandler
	}
	sched, err := parse(frequency)
	if err != nil {
		return err
	}
	if _, ok := sched.next(s.clock.Now(), s.loc); !ok {
		return fmt.Errorf("%w: %q", ErrNeverFires, strings.TrimSpace(frequency))
	}
	o := jobOptions{}
	for _, f := range opts {
		if f == nil {
			continue
		}
		if err := f(&o); err != nil {
			return err
		}
	}
	limit := s.max
	if o.maxConcurrency != nil {
		limit = *o.maxConcurrency
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.closed:
		return ErrClosed
	case s.started:
		return ErrStarted
	}
	j := &job{id: fmt.Sprintf("job-%d", len(s.jobs)+1), expr: strings.Join(strings.Fields(frequency), " "), sched: sched, handler: handler, max: limit}
	s.jobs = append(s.jobs, j)
	return nil
}

// start freezes the job list and launches the single timer loop. ctx is the
// parent of every execution context.
func (s *Scheduler) start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.closed:
		return ErrClosed
	case s.started:
		return ErrStarted
	}
	s.started = true
	s.runCtx, s.runCancel = context.WithCancel(ctx)
	now := s.clock.Now()
	for _, j := range s.jobs {
		j.next, _ = j.sched.next(now, s.loc)
	}
	go s.loop()
	s.logger.InfoContext(ctx, "[SchedulerModule] Scheduler started", "jobs", len(s.jobs), "timezone", s.loc.String(), "mode", ModeLocal)
	return nil
}

// loop is the only scheduling goroutine: it sleeps until the earliest job is
// due, dispatches every due job and computes its next firing from "now", so a
// long suspension never replays missed firings.
func (s *Scheduler) loop() {
	defer close(s.loopDone)
	for {
		var wait <-chan time.Time
		var tm timer
		if next, ok := s.earliest(); ok {
			tm = s.clock.NewTimer(max(next.Sub(s.clock.Now()), 0))
			wait = tm.C()
		}
		select {
		case <-s.stopCh:
			if tm != nil {
				tm.Stop()
			}
			return
		case <-wait:
		}
		s.fire(s.clock.Now())
	}
}

func (s *Scheduler) earliest() (time.Time, bool) {
	var first time.Time
	for _, j := range s.jobs {
		if !j.next.IsZero() && (first.IsZero() || j.next.Before(first)) {
			first = j.next
		}
	}
	return first, !first.IsZero()
}

func (s *Scheduler) fire(now time.Time) {
	for _, j := range s.jobs {
		if j.next.IsZero() || j.next.After(now) {
			continue
		}
		planned := j.next
		j.next, _ = j.sched.next(now, s.loc)
		s.dispatch(j, planned)
	}
}

func (s *Scheduler) dispatch(j *job, planned time.Time) {
	if s.runCtx.Err() != nil {
		return
	}
	if j.max > 0 && int(j.running.Load()) >= j.max {
		s.count(j, "skipped")
		s.logger.WarnContext(s.runCtx, "[SchedulerModule] Firing skipped: the job is at its concurrency limit and missed firings are not recovered",
			"job", j.id, "frequency", j.expr, "limit", j.max, "planned", planned)
		return
	}
	j.running.Add(1)
	s.wg.Add(1)
	go s.run(j, planned)
}

// run executes one firing with its own context and isolated error handling.
func (s *Scheduler) run(j *job, planned time.Time) {
	defer s.wg.Done()
	defer j.running.Add(-1)
	ctx, cancel := context.WithCancel(s.runCtx)
	defer cancel()
	start := s.clock.Now()
	outcome := "ok"
	defer func() {
		if r := recover(); r != nil {
			outcome = "panic"
			s.logger.ErrorContext(ctx, "[SchedulerModule] Job panicked", "job", j.id, "frequency", j.expr, "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
		}
		d := s.clock.Now().Sub(start)
		s.count(j, outcome)
		if s.duration != nil {
			s.duration.Observe(d.Seconds(), j.id)
		}
	}()
	s.logger.DebugContext(ctx, "[SchedulerModule] Job started", "job", j.id, "frequency", j.expr, "planned", planned)
	if err := j.handler(ctx); err != nil {
		if errors.Is(err, context.Canceled) && s.runCtx.Err() != nil {
			outcome = "canceled" // the scheduler is stopping: expected, not a failure
			s.logger.WarnContext(ctx, "[SchedulerModule] Job canceled by shutdown", "job", j.id, "frequency", j.expr, "duration", s.clock.Now().Sub(start))
			return
		}
		outcome = "error"
		s.logger.ErrorContext(ctx, "[SchedulerModule] Job failed", "job", j.id, "frequency", j.expr, "error", err, "duration", s.clock.Now().Sub(start))
		return
	}
	s.logger.DebugContext(ctx, "[SchedulerModule] Job finished", "job", j.id, "frequency", j.expr, "duration", s.clock.Now().Sub(start))
}

func (s *Scheduler) count(j *job, outcome string) {
	if s.runs != nil {
		s.runs.Inc(j.id, outcome)
	}
}

func (s *Scheduler) registerMetrics(reg *metrics.Registry) {
	s.runs = reg.Counter("nforge_scheduler_runs_total", "Scheduler job firings by job and outcome (ok, error, panic, skipped, canceled).", "job", "outcome")
	s.duration = reg.Histogram("nforge_scheduler_job_duration_seconds", "Scheduler job execution time.", nil, "job")
}

// stop ends the schedule: no further firing starts, the context of every
// running execution is canceled and the call waits for the handlers until ctx
// ends. It is idempotent; every call waits for the first and returns its result.
func (s *Scheduler) stop(ctx context.Context) error {
	s.stopOnce.Do(func() {
		defer close(s.stopped)
		s.mu.Lock()
		s.closed = true
		started := s.started
		s.mu.Unlock()
		if !started {
			return
		}
		close(s.stopCh)
		<-s.loopDone
		s.runCancel()
		done := make(chan struct{})
		go func() { s.wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-ctx.Done():
			n := 0
			for _, j := range s.jobs {
				n += int(j.running.Load())
			}
			s.stopErr = fmt.Errorf("%w: %d running: %w", ErrStopTimeout, n, ctx.Err())
		}
	})
	select {
	case <-s.stopped: // checked first: a finished stop is never reported as the caller's ctx error
		return s.stopErr
	default:
	}
	select {
	case <-s.stopped:
		return s.stopErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ready reports an error unless the scheduler is running.
func (s *Scheduler) ready(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.started || s.closed {
		return errors.New("scheduler: not running")
	}
	return nil
}
