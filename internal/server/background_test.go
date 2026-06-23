package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/indiepg/internal/alert"
	"github.com/venkatesh-sekar/indiepg/internal/telemetry"
)

// TestSeedDefaultAlertRulesIsIdempotentAndPreservesEdits proves the startup seed
// installs every shipped rule on a fresh store, never duplicates on re-run, and
// never overwrites an operator's edit (a disabled rule stays disabled).
func TestSeedDefaultAlertRulesIsIdempotentAndPreservesEdits(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := context.Background()

	// Fresh store: a seed installs exactly the default set.
	require.NoError(t, srv.seedDefaultAlertRules(ctx))
	got, err := st.ListAlerts(ctx)
	require.NoError(t, err)
	require.Len(t, got, len(alert.DefaultRules()))

	// Operator disables one rule.
	var pgDown bool
	for _, rec := range got {
		if rec.ID == "pg-down" {
			rec.Enabled = false
			require.NoError(t, st.UpsertAlert(ctx, rec))
			pgDown = true
		}
	}
	require.True(t, pgDown, "expected a default pg-down rule to disable")

	// Re-seeding must not duplicate or re-enable the operator's edit.
	require.NoError(t, srv.seedDefaultAlertRules(ctx))
	after, err := st.ListAlerts(ctx)
	require.NoError(t, err)
	require.Len(t, after, len(alert.DefaultRules()), "re-seed must not duplicate rules")
	for _, rec := range after {
		if rec.ID == "pg-down" {
			require.False(t, rec.Enabled, "re-seed must not re-enable an operator-disabled rule")
		}
	}
}

// TestStartBackgroundJobsRegistersScheduledBackups proves the runtime actually
// schedules full + incremental backups (the alert loop alone made the gap loud;
// this is the fix that makes a left-alone box back itself up). It also confirms
// the telemetry/alert loop stays registered alongside them.
func TestStartBackgroundJobsRegistersScheduledBackups(t *testing.T) {
	srv, _ := newTestServer(t)
	srv.startBackgroundJobs(context.Background())
	defer srv.stopBackgroundJobs()

	names := map[string]bool{}
	for _, j := range srv.sched.Jobs() {
		names[j.Name] = true
	}
	require.True(t, names[fullBackupJob], "full backup must be scheduled so a left-alone box still backs up")
	require.True(t, names[incrementalBackupJob], "incremental backup must be scheduled")
	require.True(t, names[telemetrySampleJob], "telemetry/alert loop must remain scheduled")
}

// TestStartBackgroundJobsEmptyScheduleDisablesBackup proves an empty schedule
// disables that backup job (the operator's explicit opt-out) rather than erroring
// or scheduling a broken job.
func TestStartBackgroundJobsEmptyScheduleDisablesBackup(t *testing.T) {
	srv, _ := newTestServer(t)
	srv.cfg.Schedules.FullBackup = ""
	srv.startBackgroundJobs(context.Background())
	defer srv.stopBackgroundJobs()

	for _, j := range srv.sched.Jobs() {
		require.NotEqual(t, fullBackupJob, j.Name, "an empty schedule must not register the job")
	}
}

// TestRunTelemetryCycleFiresAndDispatches drives one full cycle end-to-end: a
// breaching snapshot is sampled, the engine fires the backup-failed rule, and the
// firing event is delivered to a configured webhook channel. This is the proof
// that the previously-dormant alert loop now actually notifies.
func TestRunTelemetryCycleFiresAndDispatches(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := context.Background()

	require.NoError(t, srv.seedDefaultAlertRules(ctx))

	// A breaching snapshot: the most recent backup failed. backup-failed has
	// For:0, so it fires on the first cycle; nothing else breaches immediately.
	srv.collector = telemetry.NewCollector(
		telemetry.SamplerFunc(func(context.Context) (telemetry.Snapshot, error) {
			return telemetry.Snapshot{LastBackupFailed: 1}, nil
		}), st, nil, srv.log)

	// Capture webhook deliveries. The alert package's wire payload is unexported,
	// so decode into the subset of fields this test asserts on.
	type webhookDelivery struct {
		Event string `json:"event"`
		Rule  string `json:"rule"`
	}
	var (
		mu       sync.Mutex
		received []webhookDelivery
	)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var p webhookDelivery
		_ = json.Unmarshal(body, &p)
		mu.Lock()
		received = append(received, p)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ts.Close)

	// Configure an enabled webhook channel pointed at the test server.
	raw, err := json.Marshal([]alertChannelConfig{{Kind: "webhook", Enabled: true, WebhookURL: ts.URL}})
	require.NoError(t, err)
	require.NoError(t, st.SetConfig(ctx, alertChannelsConfigKey, string(raw)))

	require.NoError(t, srv.runTelemetryCycle(ctx))

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, received, 1, "expected exactly one firing notification")
	require.Equal(t, "firing", received[0].Event)
	require.Equal(t, "backup-failed", received[0].Rule)

	// The rule's firing state is persisted so the panel reflects it even though
	// delivery is the only side effect tested above.
	recs, err := st.ListAlerts(ctx)
	require.NoError(t, err)
	var firing bool
	for _, rec := range recs {
		if rec.ID == "backup-failed" {
			firing = rec.State == string(alert.StateFiring)
		}
	}
	require.True(t, firing, "backup-failed should be persisted as firing")
}

// TestRunTelemetryCycleWithoutChannelsDoesNotError proves a firing rule with no
// configured channel still completes the cycle cleanly (state persisted, nothing
// to deliver) rather than erroring or blocking.
func TestRunTelemetryCycleWithoutChannelsDoesNotError(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := context.Background()

	require.NoError(t, srv.seedDefaultAlertRules(ctx))
	srv.collector = telemetry.NewCollector(
		telemetry.SamplerFunc(func(context.Context) (telemetry.Snapshot, error) {
			return telemetry.Snapshot{LastBackupFailed: 1}, nil
		}), st, nil, srv.log)

	require.NoError(t, srv.runTelemetryCycle(ctx))

	recs, err := st.ListAlerts(ctx)
	require.NoError(t, err)
	var firing bool
	for _, rec := range recs {
		if rec.ID == "backup-failed" {
			firing = rec.State == string(alert.StateFiring)
		}
	}
	require.True(t, firing, "rule should fire and persist even with no channel to notify")
}
