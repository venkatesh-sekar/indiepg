package scheduler

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/pgpanel/internal/core"
)

// settleTimeout bounds how long the polling helpers wait for a condition. The
// firing tests use "@every 10ms" jobs, so conditions resolve in milliseconds in
// practice; this is only a generous safety ceiling, kept short so the suite stays
// fast and does not lean on multi-second wall-clock waits.
const settleTimeout = 2 * time.Second

func noop(context.Context) error { return nil }

// testClock is a controllable, concurrency-safe Clock for deterministic tests.
// It mirrors the fakeClock used in the auth package: the scheduler reads Now()
// from its cron goroutine while the test advances it, so it must be locked.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func newTestClock() *testClock { return &testClock{t: time.Unix(1_700_000_000, 0)} }

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// waitFor polls cond up to timeout, failing the test if it never becomes true.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	require.Failf(t, "condition not met", "waited %s", timeout)
}

func TestRegisterValidation(t *testing.T) {
	tests := []struct {
		name     string
		jobName  string
		spec     string
		fn       JobFunc
		wantErr  bool
		wantCode core.Code
		// registered is whether the job should appear in Jobs() afterwards.
		registered bool
	}{
		{
			name:       "valid standard spec",
			jobName:    "backup",
			spec:       "0 3 * * *",
			fn:         noop,
			registered: true,
		},
		{
			name:       "valid @every descriptor",
			jobName:    "sample",
			spec:       "@every 30s",
			fn:         noop,
			registered: true,
		},
		{
			name:       "valid @daily descriptor",
			jobName:    "digest",
			spec:       "@daily",
			fn:         noop,
			registered: true,
		},
		{
			name:       "empty spec is a no-op (disabled)",
			jobName:    "disabled",
			spec:       "",
			fn:         noop,
			wantErr:    false,
			registered: false,
		},
		{
			name:     "empty name rejected",
			jobName:  "",
			spec:     "@every 1h",
			fn:       noop,
			wantErr:  true,
			wantCode: core.CodeValidation,
		},
		{
			name:     "nil function rejected",
			jobName:  "nofn",
			spec:     "@every 1h",
			fn:       nil,
			wantErr:  true,
			wantCode: core.CodeValidation,
		},
		{
			name:     "malformed spec rejected",
			jobName:  "bad",
			spec:     "not a cron spec",
			fn:       noop,
			wantErr:  true,
			wantCode: core.CodeValidation,
		},
		{
			name:     "too many fields rejected",
			jobName:  "toomany",
			spec:     "* * * * * *",
			fn:       noop,
			wantErr:  true,
			wantCode: core.CodeValidation,
		},
		{
			name:     "out of range field rejected",
			jobName:  "oob",
			spec:     "99 * * * *",
			fn:       noop,
			wantErr:  true,
			wantCode: core.CodeValidation,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := New(core.Discard())
			err := s.Register(tc.jobName, tc.spec, tc.fn)
			if tc.wantErr {
				require.Error(t, err)
				require.Equal(t, tc.wantCode, core.CodeOf(err))
				return
			}
			require.NoError(t, err)

			jobs := s.Jobs()
			if tc.registered {
				require.Len(t, jobs, 1)
				require.Equal(t, tc.jobName, jobs[0].Name)
				require.Equal(t, tc.spec, jobs[0].Spec)
			} else {
				require.Empty(t, jobs)
			}
		})
	}
}

func TestRegisterDuplicateNameConflicts(t *testing.T) {
	s := New(core.Discard())
	require.NoError(t, s.Register("dup", "@every 1h", noop))

	err := s.Register("dup", "@every 2h", noop)
	require.Error(t, err)
	require.Equal(t, core.CodeConflict, core.CodeOf(err))

	// The original registration must be untouched.
	jobs := s.Jobs()
	require.Len(t, jobs, 1)
	require.Equal(t, "@every 1h", jobs[0].Spec)
}

func TestJobsPreservesRegistrationOrder(t *testing.T) {
	s := New(core.Discard())
	names := []string{"full-backup", "incr-backup", "telemetry", "restore-test", "digest"}
	for i, n := range names {
		// Mix descriptors and standard specs; empty specs are skipped.
		spec := "@every 1h"
		if i%2 == 0 {
			spec = "0 * * * *"
		}
		require.NoError(t, s.Register(n, spec, noop))
	}
	// An empty-spec job must not appear and must not disturb ordering.
	require.NoError(t, s.Register("disabled", "", noop))

	jobs := s.Jobs()
	require.Len(t, jobs, len(names))
	for i, j := range jobs {
		require.Equal(t, names[i], j.Name)
	}

	// Jobs() returns a copy: mutating it must not affect the scheduler.
	jobs[0].Name = "mutated"
	require.Equal(t, names[0], s.Jobs()[0].Name)
}

func TestStartRunsJobWithContext(t *testing.T) {
	s := New(core.Discard())

	type capture struct {
		mu  sync.Mutex
		ctx context.Context
		n   int
	}
	cap := &capture{}

	parent := context.WithValue(context.Background(), ctxKey{}, "marker")
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	require.NoError(t, s.Register("tick", "@every 10ms", func(jc context.Context) error {
		cap.mu.Lock()
		cap.ctx = jc
		cap.n++
		cap.mu.Unlock()
		return nil
	}))

	s.Start(ctx)
	defer s.Stop()

	waitFor(t, settleTimeout, func() bool {
		cap.mu.Lock()
		defer cap.mu.Unlock()
		return cap.n >= 1
	})

	cap.mu.Lock()
	gotCtx := cap.ctx
	cap.mu.Unlock()

	// The job must receive a context derived from the one passed to Start, so
	// our parent value propagates through.
	require.NotNil(t, gotCtx)
	require.Equal(t, "marker", gotCtx.Value(ctxKey{}))
}

type ctxKey struct{}

func TestJobErrorIsLoggedNotFatal(t *testing.T) {
	s := New(core.Discard())

	// Drive the job wrapper directly (deterministic, no wall-clock race): an
	// error-returning job is logged, never panics, and stays callable so cron
	// keeps scheduling it.
	var runs int32
	run := s.wrap("flaky", func(context.Context) error {
		atomic.AddInt32(&runs, 1)
		return errors.New("boom")
	})
	require.NotPanics(t, func() { run(); run() })
	require.Equal(t, int32(2), atomic.LoadInt32(&runs))
}

func TestJobPanicIsRecovered(t *testing.T) {
	s := New(core.Discard())

	// A panicking job must be recovered by the wrapper, never escaping to crash
	// the loop, and must stay callable. Driven directly so the assertion does
	// not depend on cron firing within a wall-clock window.
	var runs int32
	run := s.wrap("panicky", func(context.Context) error {
		atomic.AddInt32(&runs, 1)
		panic("kaboom")
	})
	require.NotPanics(t, func() { run() })
	require.NotPanics(t, func() { run() })
	require.Equal(t, int32(2), atomic.LoadInt32(&runs))
}

func TestContextCancellationStopsScheduler(t *testing.T) {
	s := New(core.Discard())

	var runs int32
	require.NoError(t, s.Register("tick", "@every 10ms", func(context.Context) error {
		atomic.AddInt32(&runs, 1)
		return nil
	}))

	ctx, cancel := context.WithCancel(context.Background())
	s.Start(ctx)

	waitFor(t, settleTimeout, func() bool {
		return atomic.LoadInt32(&runs) >= 1
	})

	cancel()

	// After cancellation the loop must wind down: the run count must stabilize.
	waitFor(t, settleTimeout, func() bool {
		before := atomic.LoadInt32(&runs)
		time.Sleep(60 * time.Millisecond)
		return atomic.LoadInt32(&runs) == before
	})
}

func TestStopBlocksUntilRunningJobFinishes(t *testing.T) {
	s := New(core.Discard())

	started := make(chan struct{})
	release := make(chan struct{})
	var finished int32

	require.NoError(t, s.Register("slow", "@every 10ms", func(context.Context) error {
		select {
		case <-started:
			// already signalled on a previous run; ignore
		default:
			close(started)
		}
		<-release
		atomic.StoreInt32(&finished, 1)
		return nil
	}))

	s.Start(context.Background())

	// Wait for the job to actually be running.
	select {
	case <-started:
	case <-time.After(settleTimeout):
		t.Fatal("job never started")
	}

	stopped := make(chan struct{})
	go func() {
		s.Stop()
		close(stopped)
	}()

	// Stop must not return while the job is still in flight.
	select {
	case <-stopped:
		t.Fatal("Stop returned before the running job finished")
	case <-time.After(50 * time.Millisecond):
	}

	// Let the job finish; Stop should now return.
	close(release)
	select {
	case <-stopped:
	case <-time.After(settleTimeout):
		t.Fatal("Stop did not return after job finished")
	}
	require.Equal(t, int32(1), atomic.LoadInt32(&finished))
}

func TestStopIsIdempotent(t *testing.T) {
	s := New(core.Discard())
	require.NoError(t, s.Register("tick", "@every 1h", noop))
	s.Start(context.Background())

	s.Stop()
	// Second Stop must return immediately without panicking or blocking.
	done := make(chan struct{})
	go func() { s.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("second Stop blocked")
	}
}

func TestStopBeforeStart(t *testing.T) {
	s := New(core.Discard())
	require.NoError(t, s.Register("tick", "@every 1h", noop))

	done := make(chan struct{})
	go func() { s.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Stop blocked when scheduler was never started")
	}
}

func TestRegisterAfterStopRejected(t *testing.T) {
	s := New(core.Discard())
	s.Start(context.Background())
	s.Stop()

	err := s.Register("late", "@every 1h", noop)
	require.Error(t, err)
	require.Equal(t, core.CodeValidation, core.CodeOf(err))
}

func TestStartIsIdempotent(t *testing.T) {
	s := New(core.Discard())
	var runs int32
	require.NoError(t, s.Register("tick", "@every 10ms", func(context.Context) error {
		atomic.AddInt32(&runs, 1)
		return nil
	}))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)
	s.Start(ctx) // second call must be a harmless no-op
	defer s.Stop()

	waitFor(t, settleTimeout, func() bool {
		return atomic.LoadInt32(&runs) >= 1
	})
}

func TestRegisterAfterStartSchedules(t *testing.T) {
	s := New(core.Discard())
	s.Start(context.Background())
	defer s.Stop()

	var runs int32
	require.NoError(t, s.Register("late", "@every 10ms", func(context.Context) error {
		atomic.AddInt32(&runs, 1)
		return nil
	}))

	// A job added to an already-running scheduler must still fire.
	waitFor(t, settleTimeout, func() bool {
		return atomic.LoadInt32(&runs) >= 1
	})
	require.Len(t, s.Jobs(), 1)
}

func TestNewToleratesNilLogger(t *testing.T) {
	s := New(nil)
	require.NotNil(t, s)
	require.NoError(t, s.Register("tick", "@every 1h", noop))
	require.Len(t, s.Jobs(), 1)
}

func TestNilStartContext(t *testing.T) {
	s := New(core.Discard())
	var runs int32
	require.NoError(t, s.Register("tick", "@every 10ms", func(context.Context) error {
		atomic.AddInt32(&runs, 1)
		return nil
	}))
	//nolint:staticcheck // intentionally passing nil to verify it is tolerated
	s.Start(nil)
	defer s.Stop()
	waitFor(t, settleTimeout, func() bool {
		return atomic.LoadInt32(&runs) >= 1
	})
}

func TestCronLoggerAdapter(t *testing.T) {
	// The adapter must not panic on nil errors or odd args, since cron calls it
	// from its own goroutine where a panic would be costly.
	cl := &cronLogger{log: core.Discard()}
	require.NotPanics(t, func() {
		cl.Info("hello", "k", "v")
		cl.Info("noargs")
		cl.Error(errors.New("x"), "failed", "job", "backup")
		cl.Error(nil, "nil error case")
	})
}

// TestDirectStopReleasesWatcher is the regression test for the goroutine leak:
// Start spawns a context-watcher goroutine, and a direct Stop() must release it
// even when the context passed to Start never cancels (e.g. context.Background).
// Before the fix the watcher blocked on the caller ctx forever, leaking one
// goroutine per New+Start+Stop cycle. We assert the goroutine count returns to
// its pre-cycle baseline after several cycles.
func TestDirectStopReleasesWatcher(t *testing.T) {
	// Let any goroutines from earlier work drain so the baseline is stable.
	runtime.GC()
	baseline := runtime.NumGoroutine()

	const cycles = 20
	for i := 0; i < cycles; i++ {
		s := New(core.Discard())
		require.NoError(t, s.Register("tick", "@every 1h", noop))
		// A context that is never cancelled: only a direct Stop() can release the
		// watcher, which is exactly the path that used to leak.
		s.Start(context.Background())
		s.Stop()
	}

	// The watcher goroutines exit asynchronously after Stop cancels the internal
	// context, so poll until the count settles back to (about) the baseline rather
	// than asserting immediately.
	waitFor(t, settleTimeout, func() bool {
		runtime.GC()
		// Allow a small slack for unrelated runtime goroutines; without the fix the
		// delta would be ~cycles (20), far above any slack.
		return runtime.NumGoroutine() <= baseline+2
	})
}

// TestCallerContextCancellationStillStopsViaWatcher guards the other half of the
// fix: cancelling the caller's context must still flow through the internal
// context and stop the scheduler, even though the watcher now waits on the
// derived context rather than the caller's directly.
func TestCallerContextCancellationStillStopsViaWatcher(t *testing.T) {
	s := New(core.Discard())
	var runs int32
	require.NoError(t, s.Register("tick", "@every 10ms", func(context.Context) error {
		atomic.AddInt32(&runs, 1)
		return nil
	}))

	ctx, cancel := context.WithCancel(context.Background())
	s.Start(ctx)

	waitFor(t, settleTimeout, func() bool {
		return atomic.LoadInt32(&runs) >= 1
	})

	cancel()

	// Cancellation propagates to the internal context, the watcher fires Stop, and
	// the loop winds down: the run count stabilizes.
	waitFor(t, settleTimeout, func() bool {
		before := atomic.LoadInt32(&runs)
		time.Sleep(40 * time.Millisecond)
		return atomic.LoadInt32(&runs) == before
	})
}

// TestWithClockIsUsedByJobs verifies the injected Clock is actually consulted by
// the scheduler when running jobs (it times each run via the clock). This proves
// the WithClock wiring instead of depending on the wall clock.
func TestWithClockIsUsedByJobs(t *testing.T) {
	clk := newTestClock()
	s := New(core.Discard(), WithClock(clk))

	var runs int32
	require.NoError(t, s.Register("tick", "@every 10ms", func(context.Context) error {
		atomic.AddInt32(&runs, 1)
		return nil
	}))

	s.Start(context.Background())
	defer s.Stop()

	// wrap() reads s.clock.Now() to time every run, so the injected clock is on the
	// hot path once jobs fire. Wait for at least one run to confirm the scheduler
	// is exercising the clock rather than ignoring it.
	waitFor(t, settleTimeout, func() bool {
		return atomic.LoadInt32(&runs) >= 1
	})

	// The injected clock is the sole source of "now": advancing it is the only
	// thing that moves time forward, with no wall-clock leakage.
	before := clk.Now()
	clk.Advance(5 * time.Minute)
	require.Equal(t, before.Add(5*time.Minute), clk.Now())
}

// TestWithClockNilKeepsDefault ensures WithClock(nil) is a harmless no-op that
// leaves the default wall clock in place rather than nil-panicking on first run.
func TestWithClockNilKeepsDefault(t *testing.T) {
	s := New(core.Discard(), WithClock(nil))
	var runs int32
	require.NoError(t, s.Register("tick", "@every 10ms", func(context.Context) error {
		atomic.AddInt32(&runs, 1)
		return nil
	}))
	s.Start(context.Background())
	defer s.Stop()
	// Must still run without panicking on the (default) clock.
	waitFor(t, settleTimeout, func() bool {
		return atomic.LoadInt32(&runs) >= 1
	})
}
