package alert

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/venkatesh-sekar/pgpanel/internal/store"
	"github.com/venkatesh-sekar/pgpanel/internal/telemetry"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// saveRule persists a rule into the store so the engine can load it.
func saveRule(t *testing.T, st *store.Store, r Rule) {
	t.Helper()
	rec, err := r.ToRecord()
	require.NoError(t, err)
	require.NoError(t, st.UpsertAlert(context.Background(), rec))
}

// cpuRule is a simple, instantly-firing CPU rule used across engine tests.
func cpuRule(forDur, cooldown time.Duration) Rule {
	return Rule{
		ID:        "cpu-high",
		Name:      "CPU high",
		Metric:    MetricCPUPercent,
		Op:        OpGT,
		Threshold: 80,
		Severity:  SeverityWarning,
		For:       forDur,
		Cooldown:  cooldown,
		Enabled:   true,
	}
}

func snapCPU(v float64) telemetry.Snapshot {
	return telemetry.Snapshot{CPUPercent: v, MaxConnections: 100}
}

func TestEngineFiresImmediatelyWhenNoForWindow(t *testing.T) {
	st := newTestStore(t)
	eng := NewEngine(st, nil)
	saveRule(t, st, cpuRule(0, 0))

	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	events, err := eng.Evaluate(context.Background(), snapCPU(95), now)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, StateFiring, events[0].State)
	require.Equal(t, "cpu-high", events[0].Rule.ID)
	require.Equal(t, 95.0, events[0].Value)
	require.Contains(t, events[0].Message, "CPU high")

	// Persisted state should be firing.
	rec, err := st.GetAlert(context.Background(), "cpu-high")
	require.NoError(t, err)
	require.Equal(t, string(StateFiring), rec.State)
	require.NotNil(t, rec.LastFiredAt)
	require.NotNil(t, rec.LastEvalAt)
}

func TestEngineNoEventWhenNotBreaching(t *testing.T) {
	st := newTestStore(t)
	eng := NewEngine(st, nil)
	saveRule(t, st, cpuRule(0, 0))

	now := time.Now().UTC()
	events, err := eng.Evaluate(context.Background(), snapCPU(10), now)
	require.NoError(t, err)
	require.Empty(t, events)

	rec, err := st.GetAlert(context.Background(), "cpu-high")
	require.NoError(t, err)
	require.Equal(t, string(StateOK), rec.State)
	require.Nil(t, rec.LastFiredAt)
	require.NotNil(t, rec.LastEvalAt) // eval timestamp is always advanced
}

func TestEngineSustainedForWindow(t *testing.T) {
	st := newTestStore(t)
	eng := NewEngine(st, nil)
	saveRule(t, st, cpuRule(5*time.Minute, 0))

	base := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)

	// First breach: within For window, must NOT fire yet.
	events, err := eng.Evaluate(context.Background(), snapCPU(95), base)
	require.NoError(t, err)
	require.Empty(t, events)

	rec, err := st.GetAlert(context.Background(), "cpu-high")
	require.NoError(t, err)
	require.Equal(t, string(StateOK), rec.State)

	// Still within the window (4 min later).
	events, err = eng.Evaluate(context.Background(), snapCPU(96), base.Add(4*time.Minute))
	require.NoError(t, err)
	require.Empty(t, events)

	// Past the window (6 min later): fire.
	events, err = eng.Evaluate(context.Background(), snapCPU(97), base.Add(6*time.Minute))
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, StateFiring, events[0].State)
}

func TestEngineForWindowResetsOnRecovery(t *testing.T) {
	st := newTestStore(t)
	eng := NewEngine(st, nil)
	saveRule(t, st, cpuRule(5*time.Minute, 0))

	base := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)

	// Breach starts.
	_, err := eng.Evaluate(context.Background(), snapCPU(95), base)
	require.NoError(t, err)

	// Dips below threshold before sustaining: timer must reset.
	_, err = eng.Evaluate(context.Background(), snapCPU(10), base.Add(2*time.Minute))
	require.NoError(t, err)

	// Breach again; 4 minutes after the *new* breach start must NOT fire.
	events, err := eng.Evaluate(context.Background(), snapCPU(95), base.Add(4*time.Minute))
	require.NoError(t, err)
	require.Empty(t, events, "for-window should have reset after the dip")

	// The new breach started at base+4m; 6 minutes after that (base+10m) fires.
	events, err = eng.Evaluate(context.Background(), snapCPU(95), base.Add(10*time.Minute))
	require.NoError(t, err)
	require.Len(t, events, 1)
}

func TestEngineCooldownSuppressesReNotify(t *testing.T) {
	st := newTestStore(t)
	eng := NewEngine(st, nil)
	saveRule(t, st, cpuRule(0, 10*time.Minute))

	base := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)

	// First fire.
	events, err := eng.Evaluate(context.Background(), snapCPU(95), base)
	require.NoError(t, err)
	require.Len(t, events, 1)

	// Still breaching, but within cooldown: suppressed.
	events, err = eng.Evaluate(context.Background(), snapCPU(96), base.Add(5*time.Minute))
	require.NoError(t, err)
	require.Empty(t, events)

	// Cooldown elapsed: re-notify.
	events, err = eng.Evaluate(context.Background(), snapCPU(96), base.Add(11*time.Minute))
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, StateFiring, events[0].State)

	// The re-notify must have refreshed LastFiredAt for the next cooldown.
	rec, err := st.GetAlert(context.Background(), "cpu-high")
	require.NoError(t, err)
	require.NotNil(t, rec.LastFiredAt)
	require.True(t, rec.LastFiredAt.Equal(base.Add(11*time.Minute)))
}

func TestEngineRecoveryEvent(t *testing.T) {
	st := newTestStore(t)
	eng := NewEngine(st, nil)
	saveRule(t, st, cpuRule(0, 10*time.Minute))

	base := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)

	// Fire.
	events, err := eng.Evaluate(context.Background(), snapCPU(95), base)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, StateFiring, events[0].State)

	// Recover: even within the firing cooldown, a recovery is always emitted.
	events, err = eng.Evaluate(context.Background(), snapCPU(10), base.Add(1*time.Minute))
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, StateResolved, events[0].State)
	require.Contains(t, events[0].Message, "RESOLVED")

	rec, err := st.GetAlert(context.Background(), "cpu-high")
	require.NoError(t, err)
	require.Equal(t, string(StateOK), rec.State)

	// A second below-threshold eval produces no event (already resolved).
	events, err = eng.Evaluate(context.Background(), snapCPU(10), base.Add(2*time.Minute))
	require.NoError(t, err)
	require.Empty(t, events)
}

func TestEngineDisabledRuleSkipped(t *testing.T) {
	st := newTestStore(t)
	eng := NewEngine(st, nil)
	r := cpuRule(0, 0)
	r.Enabled = false
	saveRule(t, st, r)

	events, err := eng.Evaluate(context.Background(), snapCPU(99), time.Now().UTC())
	require.NoError(t, err)
	require.Empty(t, events)
}

func TestEngineUnknownMetricLeavesStateUntouched(t *testing.T) {
	st := newTestStore(t)
	eng := NewEngine(st, nil)
	r := cpuRule(0, 0)
	r.Metric = "does.not.exist"
	saveRule(t, st, r)

	events, err := eng.Evaluate(context.Background(), snapCPU(99), time.Now().UTC())
	require.NoError(t, err)
	require.Empty(t, events)

	rec, err := st.GetAlert(context.Background(), "cpu-high")
	require.NoError(t, err)
	require.Equal(t, string(StateOK), rec.State) // unchanged
}

func TestEngineMissingMetricDoesNotFalseRecover(t *testing.T) {
	st := newTestStore(t)
	eng := NewEngine(st, nil)
	// A rule on a percentage metric that becomes uncomputable mid-stream.
	r := Rule{
		ID:        "disk-full",
		Name:      "Disk full",
		Metric:    MetricDiskPercent,
		Op:        OpGTE,
		Threshold: 90,
		Severity:  SeverityCritical,
		Enabled:   true,
	}
	saveRule(t, st, r)

	base := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	// Fire on a full disk.
	events, err := eng.Evaluate(context.Background(),
		telemetry.Snapshot{DiskUsedBytes: 95, DiskTotalBytes: 100, MaxConnections: 100}, base)
	require.NoError(t, err)
	require.Len(t, events, 1)

	// Next snapshot has no disk totals (sampler failed): metric uncomputable.
	// The rule must stay firing, not falsely "recover".
	events, err = eng.Evaluate(context.Background(),
		telemetry.Snapshot{MaxConnections: 100}, base.Add(time.Minute))
	require.NoError(t, err)
	require.Empty(t, events)

	rec, err := st.GetAlert(context.Background(), "disk-full")
	require.NoError(t, err)
	require.Equal(t, string(StateFiring), rec.State)
}

func TestEngineMultipleRules(t *testing.T) {
	st := newTestStore(t)
	eng := NewEngine(st, nil)
	saveRule(t, st, cpuRule(0, 0))
	saveRule(t, st, Rule{
		ID:        "lag-high",
		Name:      "Replication lag high",
		Metric:    MetricReplicationLagSecs,
		Op:        OpGT,
		Threshold: 60,
		Severity:  SeverityWarning,
		Enabled:   true,
	})

	now := time.Now().UTC()
	snap := telemetry.Snapshot{CPUPercent: 95, ReplicationLagSeconds: 120, MaxConnections: 100}
	events, err := eng.Evaluate(context.Background(), snap, now)
	require.NoError(t, err)
	require.Len(t, events, 2)

	ids := map[string]bool{}
	for _, ev := range events {
		ids[ev.Rule.ID] = true
		require.Equal(t, StateFiring, ev.State)
	}
	require.True(t, ids["cpu-high"])
	require.True(t, ids["lag-high"])
}

func TestEngineMalformedRuleSkippedNotFatal(t *testing.T) {
	st := newTestStore(t)
	eng := NewEngine(st, nil)

	// Insert a deliberately malformed record bypassing ToRecord validation.
	require.NoError(t, st.UpsertAlert(context.Background(), store.AlertRecord{
		ID:         "broken",
		Name:       "broken",
		Enabled:    true,
		Definition: `{not valid json`,
		Severity:   string(SeverityWarning),
		State:      string(StateOK),
	}))
	// And a valid rule alongside it.
	saveRule(t, st, cpuRule(0, 0))

	events, err := eng.Evaluate(context.Background(), snapCPU(95), time.Now().UTC())
	require.NoError(t, err, "a malformed rule must not abort the cycle")
	require.Len(t, events, 1)
	require.Equal(t, "cpu-high", events[0].Rule.ID)
}

func TestEngineNoRules(t *testing.T) {
	st := newTestStore(t)
	eng := NewEngine(st, nil)
	events, err := eng.Evaluate(context.Background(), fullSnapshot(), time.Now().UTC())
	require.NoError(t, err)
	require.Empty(t, events)
}

func TestEngineStatePersistsAcrossEngines(t *testing.T) {
	st := newTestStore(t)
	saveRule(t, st, cpuRule(0, 30*time.Minute))
	base := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)

	eng1 := NewEngine(st, nil)
	events, err := eng1.Evaluate(context.Background(), snapCPU(95), base)
	require.NoError(t, err)
	require.Len(t, events, 1)

	// A fresh engine (simulating a restart) reads firing state from the store
	// and honors the cooldown — no duplicate notification.
	eng2 := NewEngine(st, nil)
	events, err = eng2.Evaluate(context.Background(), snapCPU(96), base.Add(5*time.Minute))
	require.NoError(t, err)
	require.Empty(t, events, "cooldown should carry over across a restart")
}
