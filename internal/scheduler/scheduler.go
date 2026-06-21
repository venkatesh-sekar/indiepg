// Package scheduler is a thin wrapper over robfig/cron/v3 that gives pgpanel a
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
//     whole panel down. Panics inside a job are recovered (via cron's Recover
//     chain) so one misbehaving job cannot crash the loop either.
//   - An empty spec disables a job (registration is a no-op) so callers can
//     wire config-driven schedules without branching at every call site.
package scheduler

import (
	"context"
	"sync"

	cron "github.com/robfig/cron/v3"

	"github.com/venkatesh-sekar/pgpanel/internal/core"
)

// JobFunc is a scheduled unit of work. Returned errors are logged, not fatal,
// so a single failed run never stops the scheduler or the panel.
type JobFunc func(ctx context.Context) error

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
	log *core.Logger

	mu      sync.Mutex
	cron    *cron.Cron
	jobs    map[string]registered
	order   []string // registration order, for stable Jobs() output
	ctx     context.Context
	started bool
	stopped bool
}

// New constructs a Scheduler. The cron loop is created but not started; call
// Start to begin firing jobs. A nil logger is tolerated (logging is discarded).
func New(log *core.Logger) *Scheduler {
	if log == nil {
		log = core.Discard()
	}
	s := &Scheduler{
		log:  log,
		jobs: make(map[string]registered),
		ctx:  context.Background(),
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
		ctx := s.currentContext()
		// Bail out immediately if the scheduler context is already cancelled,
		// e.g. a tick raced with Stop.
		if err := ctx.Err(); err != nil {
			s.log.Debug("scheduler: skipping job, context done", "job", name)
			return
		}
		s.log.Debug("scheduler: job starting", "job", name)
		if err := fn(ctx); err != nil {
			// Errors are logged, never fatal: a failed backup or sample must not
			// stop the loop or the panel.
			s.log.Error("scheduler: job failed", "job", name, "error", err.Error())
			return
		}
		s.log.Debug("scheduler: job completed", "job", name)
	}
}

// currentContext returns the context jobs should run under, guarding the read
// with the mutex so Start/Stop swaps are observed safely.
func (s *Scheduler) currentContext() context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	s.started = true
	s.ctx = ctx
	c := s.cron
	jobCount := len(s.order)
	s.mu.Unlock()

	c.Start()
	s.log.Info("scheduler: started", "jobs", jobCount)

	// Watch the context: when it is cancelled, stop the cron loop so we honor
	// "ctx cancellation stops it" without the caller needing to call Stop.
	go func() {
		<-ctx.Done()
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
	s.mu.Unlock()

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
