// Package scheduler is a thin wrapper over robfig/cron/v3 that gives indiepg a
// small, named job registry for its internal cron needs (backups, telemetry
// sampling, restore-testing, digests). The panel never shells out to the
// system cron; everything runs in-process so the single binary stays
// self-contained.
//
// Design notes:
//   - Jobs are context-aware (JobFunc takes a context.Context). robfig/cron's
//     native job type is a bare func(), so we bridge the two: the context
//     handed to a job is the one passed to Start, cancelled when Start's ctx is
//     cancelled or Stop is called.
//   - Job errors are logged, never fatal — a failing backup must not take the
//     whole panel down. Panics inside a job are recovered in the job wrapper
//     (with cron's Recover chain kept as a backstop) so one misbehaving job
//     cannot crash the loop either.
//   - An empty spec disables a job (registration is a no-op) so callers can
//     wire config-driven schedules without branching at every call site.
package scheduler

import (
	"context"
	"fmt"
	"sync"
	"time"

	cron "github.com/robfig/cron/v3"

	"github.com/venkatesh-sekar/indiepg/internal/core"
)

// JobFunc is a scheduled unit of work. Returned errors are logged, not fatal,
// so a single failed run never stops the scheduler or the panel.
type JobFunc func(ctx context.Context) error

// Clock is the time source the scheduler reads "now" from. It mirrors the small
// clock idiom used elsewhere in the codebase (auth, identity) so tests can drive
// time deterministically instead of sleeping against the wall clock.
//
// Note: the underlying robfig/cron/v3 (v3.0.1) does not expose a clock seam, so
// the cron loop itself still fires on the real wall clock. The scheduler clock
// governs the time-related decisions the scheduler owns directly (and lets tests
// assert the wiring); it is not a way to virtualize cron tick timing.
type Clock interface {
	// Now returns the current time.
	Now() time.Time
}

// realClock is the default Clock, backed by the wall clock.
type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// Option configures a Scheduler at construction time.
type Option func(*Scheduler)

// WithClock overrides the scheduler's time source. The default is a wall-clock
// clock; tests inject a controllable clock so they do not depend on real time.
// A nil clock is ignored, keeping the default.
func WithClock(clk Clock) Option {
	return func(s *Scheduler) {
		if clk != nil {
			s.clock = clk
		}
	}
}

// Job is a registered cron job as seen by callers (no behavior, just metadata).
type Job struct {
	// Name is the unique, human-readable identifier for the job.
	Name string
	// Spec is the robfig/cron expression (standard 5-field) or an @-descriptor
	// such as "@every 1h" or "@daily".
	Spec string
}

// registered pairs the public Job metadata with the cron entry id assigned when
// the job was added, so the registry can report jobs in a stable order.
type registered struct {
	job Job
	id  cron.EntryID
}

// Scheduler wraps a robfig/cron/v3 cron with a named job registry.
//
// A Scheduler is safe for concurrent use: Register, Jobs, Start and Stop may be
// called from different goroutines. Register may be called before or after
// Start; cron supports adding entries to an already-running cron.
type Scheduler struct {
	log   *core.Logger
	clock Clock

	mu sync.Mutex
	// icancel cancels the internal context derived in Start. It is what lets a
	// direct Stop() release the context-watcher goroutine: without it the watcher
	// would block on the caller's ctx (which may never be cancelled, e.g.
	// context.Background()) and leak until process exit.
	icancel context.CancelFunc
	cron    *cron.Cron
	jobs    map[string]registered
	order   []string // registration order, for stable Jobs() output
	ctx     context.Context
	started bool
	stopped bool
}

// New constructs a Scheduler. The cron loop is created but not started; call
// Start to begin firing jobs. A nil logger is tolerated (logging is discarded).
// Options (e.g. WithClock) tune behavior; with none, the wall clock is used.
func New(log *core.Logger, opts ...Option) *Scheduler {
	if log == nil {
		log = core.Discard()
	}
	s := &Scheduler{
		log:   log,
		clock: realClock{},
		jobs:  make(map[string]registered),
		ctx:   context.Background(),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	// Recover wraps every job so a panic is logged and swallowed rather than
	// crashing the cron goroutine. We route cron's own logging through our
	// structured logger as well.
	cl := &cronLogger{log: log}
	s.cron = cron.New(cron.WithLogger(cl), cron.WithChain(cron.Recover(cl)))
	return s
}

// Register adds a named job with a cron spec. An empty spec disables the job:
// registration becomes a no-op (no error), so callers can pass a config value
// straight through. A duplicate name returns *core.Error CodeConflict; a
// malformed spec returns *core.Error CodeValidation. A nil fn is rejected with
// CodeValidation.
//
// Register may be called before or after Start.
func (s *Scheduler) Register(name, spec string, fn JobFunc) error {
	if name == "" {
		return core.ValidationError("job name must not be empty")
	}
	if spec == "" {
		// Disabled job: nothing scheduled, and we deliberately do not record it
		// so Jobs() reflects only what will actually run.
		s.log.Debug("scheduler: job disabled (empty spec)", "job", name)
		return nil
	}
	if fn == nil {
		return core.ValidationError("job %q has a nil function", name)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.stopped {
		return core.ValidationError("scheduler is stopped; cannot register %q", name)
	}
	if _, exists := s.jobs[name]; exists {
		return core.ConflictError("job %q is already registered", name)
	}

	// Validate the spec up front so a bad expression is reported as a typed
	// validation error rather than silently dropped. cron.ParseStandard uses
	// the same default parser (5-field + @descriptors) as cron.New.
	if _, err := cron.ParseStandard(spec); err != nil {
		return core.ValidationError("invalid cron spec %q for job %q: %v", spec, name, err).Wrap(err)
	}

	job := Job{Name: name, Spec: spec}
	id, err := s.cron.AddFunc(spec, s.wrap(name, fn))
	if err != nil {
		// Should be unreachable given the ParseStandard check above, but never
		// panic in library code — surface it as a typed error.
		return core.ValidationError("could not schedule job %q with spec %q: %v", name, spec, err).Wrap(err)
	}

	s.jobs[name] = registered{job: job, id: id}
	s.order = append(s.order, name)
	s.log.Debug("scheduler: job registered", "job", name, "spec", spec)
	return nil
}

// wrap adapts a context-aware JobFunc to cron's bare func(). It injects the
// scheduler's current context, logs any error, and times the run for debug
// visibility. Panics are handled by the Recover chain installed in New.
func (s *Scheduler) wrap(name string, fn JobFunc) func() {
	return func() {
		// Recover panics in the job itself so a misbehaving job can never crash
		// the scheduler loop or the panel. This is the authoritative recovery
		// boundary (cron's Recover chain is kept as belt-and-suspenders), which
		// also makes the behaviour unit-testable without driving the cron clock.
		defer func() {
			if r := recover(); r != nil {
				s.log.Error("scheduler: job panicked (recovered)", "job", name, "panic", fmt.Sprintf("%v", r))
			}
		}()

		ctx := s.currentContext()
		// Bail out immediately if the scheduler context is already cancelled,
		// e.g. a tick raced with Stop.
		if err := ctx.Err(); err != nil {
			s.log.Debug("scheduler: skipping job, context done", "job", name)
			return
		}
		start := s.clock.Now()
		s.log.Debug("scheduler: job starting", "job", name)
		if err := fn(ctx); err != nil {
			// Errors are logged, never fatal: a failed backup or sample must not
			// stop the loop or the panel.
			s.log.Error("scheduler: job failed", "job", name,
				"error", err.Error(), "took", s.clock.Now().Sub(start).String())
			return
		}
		s.log.Debug("scheduler: job completed", "job", name,
			"took", s.clock.Now().Sub(start).String())
	}
}

// currentContext returns the context jobs should run under, guarding the read
// with the mutex so Start/Stop swaps are observed safely.
func (s *Scheduler) currentContext() context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Default to Background when the scheduler has not been Started yet (or a
	// job runnable is invoked directly), so jobs never receive a nil context.
	if s.ctx == nil {
		return context.Background()
	}
	return s.ctx
}

// Jobs returns the registered (enabled) jobs in registration order. The result
// is a copy; mutating it does not affect the scheduler.
func (s *Scheduler) Jobs() []Job {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]Job, 0, len(s.order))
	for _, name := range s.order {
		if r, ok := s.jobs[name]; ok {
			out = append(out, r.job)
		}
	}
	return out
}

// Start begins the cron loop. It is non-blocking: cron runs its own goroutine.
// The provided context governs the lifetime of jobs — when ctx is cancelled the
// scheduler stops the loop and the context handed to running/future jobs is
// done. Calling Start more than once is a no-op after the first call.
func (s *Scheduler) Start(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}

	s.mu.Lock()
	if s.started || s.stopped {
		s.mu.Unlock()
		return
	}
	// Derive an internal cancellable context from the caller's. The watcher waits
	// on ictx, which is cancelled either when the caller's ctx is cancelled
	// (context.WithCancel propagates parent cancellation) or when Stop() calls
	// icancel directly. This guarantees a direct Stop() releases the watcher even
	// when the caller passed a context that never cancels (e.g.
	// context.Background()), closing the goroutine leak.
	ictx, icancel := context.WithCancel(ctx)
	s.started = true
	s.ctx = ctx
	s.icancel = icancel
	c := s.cron
	jobCount := len(s.order)
	s.mu.Unlock()

	c.Start()
	s.log.Info("scheduler: started", "jobs", jobCount)

	// Watch the internal context: when it is done — caller-ctx cancellation or a
	// direct Stop — stop the cron loop so we honor "ctx cancellation stops it"
	// without the caller needing to call Stop, and so the watcher always exits.
	go func() {
		<-ictx.Done()
		s.Stop()
	}()
}

// Stop halts the cron loop and blocks until any currently-running jobs finish.
// It is idempotent: a second call returns immediately. After Stop the scheduler
// cannot be restarted and Register is rejected.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	c := s.cron
	started := s.started
	icancel := s.icancel
	s.mu.Unlock()

	// Release the context-watcher goroutine spawned in Start. icancel cancels the
	// internal context it waits on; without this a direct Stop() (the documented,
	// complete shutdown path) would leak the watcher whenever the caller's ctx
	// never cancels. Safe to call when Start was never reached (icancel is nil).
	if icancel != nil {
		icancel()
	}

	if !started {
		// Never started: nothing is running, mark stopped and return.
		s.log.Debug("scheduler: stop called before start")
		return
	}

	// cron.Stop returns a context that is done once running jobs complete; wait
	// for it so callers can rely on a clean shutdown.
	done := c.Stop()
	<-done.Done()
	s.log.Info("scheduler: stopped")
}

// cronLogger adapts cron's Logger interface onto core.Logger so cron's internal
// messages and recovered panics flow through the panel's structured logging.
type cronLogger struct {
	log *core.Logger
}

// Info implements cron.Logger.
func (c *cronLogger) Info(msg string, keysAndValues ...any) {
	c.log.Debug("cron: "+msg, keysAndValues...)
}

// Error implements cron.Logger.
func (c *cronLogger) Error(err error, msg string, keysAndValues ...any) {
	args := make([]any, 0, len(keysAndValues)+2)
	args = append(args, "error")
	if err != nil {
		args = append(args, err.Error())
	} else {
		args = append(args, "<nil>")
	}
	args = append(args, keysAndValues...)
	c.log.Error("cron: "+msg, args...)
}
