package alert

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/venkatesh-sekar/indiepg/internal/store"
	"github.com/venkatesh-sekar/indiepg/internal/telemetry"
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

// TestEngineFiresOnFailedBackup proves the shipped "backup-failed" default rule
// fires loudly and immediately when the most recent backup attempt failed
// (LastBackupFailed=1) and recovers once a backup succeeds again. This is the
// "loud alert when a scheduled backup fails" durability guarantee.
func TestEngineFiresOnFailedBackup(t *testing.T) {
	st := newTestStore(t)
	eng := NewEngine(st, nil)

	var rule Rule
	for _, r := range DefaultRules() {
		if r.ID == "backup-failed" {
			rule = r
		}
	}
	require.Equal(t, "backup-failed", rule.ID, "default rule set must include backup-failed")
	require.Equal(t, SeverityCritical, rule.Severity, "a failed backup is a critical durability event")
	require.Zero(t, rule.For, "a failed backup must fire immediately, not after a sustained window")
	saveRule(t, st, rule)

	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)

	// Latest backup failed: fire immediately.
	events, err := eng.Evaluate(context.Background(),
		telemetry.Snapshot{LastBackupFailed: 1, MaxConnections: 100}, now)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, StateFiring, events[0].State)
	require.Equal(t, "backup-failed", events[0].Rule.ID)

	// A subsequent successful backup clears the signal: recovery event.
	events, err = eng.Evaluate(context.Background(),
		telemetry.Snapshot{LastBackupFailed: 0, MaxConnections: 100}, now.Add(time.Minute))
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, StateResolved, events[0].State)
}

// TestDiskHeadroomEarlyWarningTier proves the shipped disk rules form a two-tier
// escalation: "disk-headroom-low" is a Warning that fires WELL BEFORE the
// "disk-almost-full" Critical, giving the operator runway to act before a slow
// fill becomes an emergency that can stop Postgres. It verifies the tier ordering
// and that the early warning actually fires (after its For window) at a disk
// level that does NOT yet breach the critical threshold.
func TestDiskHeadroomEarlyWarningTier(t *testing.T) {
	var warn, crit Rule
	for _, r := range DefaultRules() {
		switch r.ID {
		case "disk-headroom-low":
			warn = r
		case "disk-almost-full":
			crit = r
		}
	}
	require.Equal(t, "disk-headroom-low", warn.ID, "default rule set must include the early-warning tier")
	require.Equal(t, "disk-almost-full", crit.ID, "default rule set must include the critical tier")

	// Tier shape: the warning must be lower-severity and trip at a strictly lower
	// threshold than the critical, so it is a genuine early warning.
	require.Equal(t, SeverityWarning, warn.Severity)
	require.Equal(t, SeverityCritical, crit.Severity)
	require.Equal(t, MetricDiskPercent, warn.Metric)
	require.Equal(t, MetricDiskPercent, crit.Metric)
	require.Less(t, warn.Threshold, crit.Threshold, "the warning must fire before the critical threshold")

	// Non-vacuous firing: at a disk level between the two thresholds, the early
	// warning fires (once sustained past its For window) while the critical does
	// not. Use 85% (80 <= 85 < 90).
	st := newTestStore(t)
	eng := NewEngine(st, nil)
	saveRule(t, st, warn)
	saveRule(t, st, crit)

	// 85GiB used of 100GiB == 85% disk.
	snap85 := telemetry.Snapshot{DiskUsedBytes: 85, DiskTotalBytes: 100, MaxConnections: 100}
	base := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)

	// First breach: within the warning's For window, nothing fires yet.
	events, err := eng.Evaluate(context.Background(), snap85, base)
	require.NoError(t, err)
	require.Empty(t, events)

	// Sustained past the 5-minute For window: only the warning fires; the
	// critical stays quiet because 85% < 90%.
	events, err = eng.Evaluate(context.Background(), snap85, base.Add(6*time.Minute))
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, "disk-headroom-low", events[0].Rule.ID)
	require.Equal(t, StateFiring, events[0].State)

	critRec, err := st.GetAlert(context.Background(), "disk-almost-full")
	require.NoError(t, err)
	require.Equal(t, string(StateOK), critRec.State, "critical tier must not fire below its threshold")
}

// TestConnectionSaturationTiers proves the shipped connection-saturation rules
// form a two-tier escalation like the disk rules: "connections-near-max" is a
// Warning that trips at a strictly lower saturation than the
// "connections-critical" Critical, which pages louder and sooner as the box
// approaches max_connections (a hard outage: Postgres refuses new clients). It
// verifies the tier ordering and that BOTH tiers fire non-vacuously — the
// warning between the thresholds without tripping the critical, and the critical
// once saturation is near-total.
func TestConnectionSaturationTiers(t *testing.T) {
	var warn, crit Rule
	for _, r := range DefaultRules() {
		switch r.ID {
		case "connections-near-max":
			warn = r
		case "connections-critical":
			crit = r
		}
	}
	require.Equal(t, "connections-near-max", warn.ID, "default rule set must include the warning tier")
	require.Equal(t, "connections-critical", crit.ID, "default rule set must include the critical tier")

	// Tier shape: the warning must be lower-severity and trip at a strictly lower
	// saturation than the critical, so it is a genuine early warning on the same
	// metric.
	require.Equal(t, SeverityWarning, warn.Severity)
	require.Equal(t, SeverityCritical, crit.Severity)
	require.Equal(t, MetricConnectionsPercent, warn.Metric)
	require.Equal(t, MetricConnectionsPercent, crit.Metric)
	require.Less(t, warn.Threshold, crit.Threshold, "the warning must fire before the critical threshold")

	base := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)

	// Warning-only band: at saturation between the two thresholds (90 of 100 ==
	// 90%, with 85 <= 90 < 95) the warning fires once sustained past its For
	// window while the critical stays quiet.
	t.Run("warning fires below critical", func(t *testing.T) {
		st := newTestStore(t)
		eng := NewEngine(st, nil)
		saveRule(t, st, warn)
		saveRule(t, st, crit)

		snap90 := telemetry.Snapshot{Connections: 90, MaxConnections: 100}

		// First breach: within the warning's For window, nothing fires yet.
		events, err := eng.Evaluate(context.Background(), snap90, base)
		require.NoError(t, err)
		require.Empty(t, events)

		// Sustained past the 2-minute For window: only the warning fires.
		events, err = eng.Evaluate(context.Background(), snap90, base.Add(3*time.Minute))
		require.NoError(t, err)
		require.Len(t, events, 1)
		require.Equal(t, "connections-near-max", events[0].Rule.ID)
		require.Equal(t, StateFiring, events[0].State)

		critRec, err := st.GetAlert(context.Background(), "connections-critical")
		require.NoError(t, err)
		require.Equal(t, string(StateOK), critRec.State, "critical tier must not fire below its threshold")
	})

	// Critical band: at near-total saturation (97 of 100 == 97% >= 95) the
	// critical tier fires once sustained past its (shorter) For window. Proves the
	// new tier is non-vacuous.
	t.Run("critical fires near saturation", func(t *testing.T) {
		st := newTestStore(t)
		eng := NewEngine(st, nil)
		saveRule(t, st, crit)

		snap97 := telemetry.Snapshot{Connections: 97, MaxConnections: 100}

		// First breach: within the critical's For window, nothing fires yet.
		events, err := eng.Evaluate(context.Background(), snap97, base)
		require.NoError(t, err)
		require.Empty(t, events)

		// Sustained past the 1-minute For window: the critical fires.
		events, err = eng.Evaluate(context.Background(), snap97, base.Add(90*time.Second))
		require.NoError(t, err)
		require.Len(t, events, 1)
		require.Equal(t, "connections-critical", events[0].Rule.ID)
		require.Equal(t, SeverityCritical, events[0].Rule.Severity)
		require.Equal(t, StateFiring, events[0].State)
	})
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

// failingUpsertStore wraps an in-memory rule list and fails UpsertAlert for one
// chosen rule ID, so a test can prove a single rule's persist failure does not
// take down the rest of the cycle. Records are returned in the same name order
// the real store guarantees (ORDER BY name).
type failingUpsertStore struct {
	records      []store.AlertRecord
	failUpsertID string
	upserted     map[string]store.AlertRecord // successful writes, by ID
}

func (f *failingUpsertStore) ListAlerts(context.Context) ([]store.AlertRecord, error) {
	return f.records, nil
}

func (f *failingUpsertStore) UpsertAlert(_ context.Context, a store.AlertRecord) error {
	if a.ID == f.failUpsertID {
		return errors.New("simulated store write failure")
	}
	if f.upserted == nil {
		f.upserted = map[string]store.AlertRecord{}
	}
	f.upserted[a.ID] = a
	return nil
}

// TestEvaluateOneRulePersistFailureKeepsSiblingEvents proves that when one rule's
// state cannot be written back (a transient store error mid-cycle), the engine
// still returns and persists every OTHER rule's firing — and still surfaces the
// failing rule's own event. Before the fix, a single persist error did
// `return nil, err`, discarding every already-computed event for that cycle, so a
// critical page (e.g. disk-almost-full) could be silently dropped because an
// unrelated rule could not be saved.
func TestEvaluateOneRulePersistFailureKeepsSiblingEvents(t *testing.T) {
	// The failing rule sorts FIRST by name, so it is processed before the good
	// one — proving the loop does not abort on the first persist failure.
	failRule := Rule{
		ID: "aaa-fail", Name: "AAA failing rule",
		Metric: MetricCPUPercent, Op: OpGT, Threshold: 80,
		Severity: SeverityWarning, Enabled: true,
	}
	goodRule := Rule{
		ID: "zzz-good", Name: "ZZZ good rule",
		Metric: MetricDiskPercent, Op: OpGT, Threshold: 80,
		Severity: SeverityCritical, Enabled: true,
	}
	failRec, err := failRule.ToRecord()
	require.NoError(t, err)
	goodRec, err := goodRule.ToRecord()
	require.NoError(t, err)

	st := &failingUpsertStore{
		records:      []store.AlertRecord{failRec, goodRec}, // name order
		failUpsertID: "aaa-fail",
	}
	eng := NewEngine(st, nil)

	// Both rules breach and fire immediately (no For window).
	snap := telemetry.Snapshot{CPUPercent: 95, DiskUsedBytes: 95, DiskTotalBytes: 100, MaxConnections: 100}
	events, err := eng.Evaluate(context.Background(), snap, time.Now().UTC())

	// The cycle reports the persist failure...
	require.Error(t, err, "the failing rule's persist error must be surfaced, not swallowed")
	// ...but BOTH events are still returned for dispatch (append-before-persist):
	// the sibling's event must never be lost to the other rule's bad write.
	ids := map[string]State{}
	for _, ev := range events {
		ids[ev.Rule.ID] = ev.State
	}
	require.Equal(t, StateFiring, ids["zzz-good"], "sibling rule's firing must survive the other rule's persist failure")
	require.Equal(t, StateFiring, ids["aaa-fail"], "the failing rule's own event must still be dispatched")
	require.Len(t, events, 2)

	// The sibling's state was actually persisted (the loop continued past the
	// failure), while the failing rule was not written.
	require.Contains(t, st.upserted, "zzz-good", "the sibling rule's state must be persisted")
	require.NotContains(t, st.upserted, "aaa-fail", "the failing rule's write did not succeed")
}
