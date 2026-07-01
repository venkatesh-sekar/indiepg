package backup

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/exec"
)

// sampleInfoJSON's earliest backup started 2026-01-01 03:00:00 UTC (unix
// 1767236400). These tests pin the recovery-target preflight to that boundary.
var earliestSampleBackupStart = time.Date(2026, 1, 1, 3, 0, 0, 0, time.UTC)

// TestManagerRestore_RejectsTargetBeforeEarliestBackup proves a TIME target that
// predates the earliest available backup is rejected LOUDLY and EARLY — before
// any destructive step. Recovery replays WAL forward from a base backup, so a
// target earlier than every backup can never be reached; letting pgBackRest
// discover that would fail partway through, after the live cluster is already
// stopped and a safety backup taken. The preflight refuses up front instead.
func TestManagerRestore_RejectsTargetBeforeEarliestBackup(t *testing.T) {
	events := &[]string{}
	m, fake := newRestoreManager(t, events, &recordingCluster{events: events}, nil)

	before := earliestSampleBackupStart.Add(-1 * time.Hour)
	_, err := m.Restore(context.Background(), &RecoveryTarget{Time: &before}, false, "main")

	require.Error(t, err)
	require.Equal(t, core.CodeValidation, core.CodeOf(err),
		"an unreachable target is an operator/validation error, not an exec failure")
	require.Contains(t, err.Error(), "before the earliest available backup",
		"the error must name why the target is unreachable")

	// The whole point of a preflight: nothing destructive runs. The cluster is
	// never stopped, and neither the safety backup nor the restore is issued.
	require.Empty(t, *events,
		"a rejected target must not stop/start the live cluster")
	for _, c := range fake.Calls() {
		joined := strings.Join(c.Args, " ")
		require.NotContains(t, joined, "restore",
			"pgBackRest restore must not run for an unreachable target")
		require.NotContains(t, joined, "backup",
			"the destructive safety backup must not run for an unreachable target")
	}
}

// TestManagerRestore_AllowsTargetAtEarliestBackupStart proves the preflight is
// conservative: a target exactly at the earliest backup's start is NOT rejected.
// The guard only refuses targets it can PROVE are unreachable (strictly earlier
// than every backup); anything at/after the earliest backup is left to pgBackRest,
// the final arbiter. This guarantees the preflight never false-rejects a
// legitimately recoverable target.
func TestManagerRestore_AllowsTargetAtEarliestBackupStart(t *testing.T) {
	events := &[]string{}
	m, _ := newRestoreManager(t, events, &recordingCluster{events: events}, nil)

	atStart := earliestSampleBackupStart
	res, err := m.Restore(context.Background(), &RecoveryTarget{Time: &atStart}, false, "main")

	require.NoError(t, err)
	require.True(t, res.OK)
	require.Equal(t, []string{"stop", "restore", "start"}, *events,
		"a target at/after the earliest backup proceeds through the normal restore sequence")
}

// TestManagerRestore_PreflightSkipsNonTimeTarget locks the TIME-only scope: an
// xid/lsn/name target has no wall-clock to compare against backup start times, so
// the preflight must leave it entirely to pgBackRest and proceed with the restore.
// This guards against a future refactor that starts (wrongly) range-checking
// non-TIME targets here.
func TestManagerRestore_PreflightSkipsNonTimeTarget(t *testing.T) {
	events := &[]string{}
	m, _ := newRestoreManager(t, events, &recordingCluster{events: events}, nil)

	res, err := m.Restore(context.Background(), &RecoveryTarget{XID: "1"}, false, "main")

	require.NoError(t, err)
	require.True(t, res.OK)
	require.Equal(t, []string{"stop", "restore", "start"}, *events,
		"a non-TIME target is not preflighted and proceeds through the normal restore")
}

// TestManagerRestore_PreflightFailsOpenWhenInfoUnavailable proves the preflight
// fails OPEN: if the repo can't be enumerated, even a target that would otherwise
// be rejected proceeds. Wrongly refusing recovery (the most data-critical
// operation) on a transient `info` hiccup would be worse than a late pgBackRest
// error — so uncertainty defers to pgBackRest rather than blocking.
func TestManagerRestore_PreflightFailsOpenWhenInfoUnavailable(t *testing.T) {
	events := &[]string{}
	fake := exec.NewFakeRunner()
	fake.On("backup", exec.FakeResponse{Stdout: "ok"})
	fake.On("info", exec.FakeResponse{ExitCode: 1, Err: core.ExecError("info boom")})
	fake.On("restore", exec.FakeResponse{Stdout: "restored"})
	m := New(Options{
		Runner:  &orderRunner{inner: fake, events: events},
		Store:   newTestStore(t),
		Config:  testConfig(),
		Logger:  core.Discard(),
		Cluster: &recordingCluster{events: events},
	})

	before := earliestSampleBackupStart.Add(-1 * time.Hour)
	res, err := m.Restore(context.Background(), &RecoveryTarget{Time: &before}, false, "main")

	require.NoError(t, err, "an info failure must not block recovery")
	require.True(t, res.OK)
	require.Equal(t, []string{"stop", "restore", "start"}, *events,
		"the restore proceeds when the target can't be preflighted")
}

// TestEarliestBackupStart locks the boundary helper: it returns the minimum
// non-zero start time and ignores backups with an unknown (zero) start, and
// reports not-found when no backup carries a usable start time — the branch that
// makes the preflight fail open on incomplete pgBackRest metadata.
func TestEarliestBackupStart(t *testing.T) {
	newer := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	older := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	got, ok := earliestBackupStart([]BackupInfo{
		{Label: "newer", StartTime: newer},
		{Label: "zero", StartTime: time.Time{}}, // unknown start — must be ignored
		{Label: "older", StartTime: older},
	})
	require.True(t, ok)
	require.Equal(t, older, got, "earliest is the minimum non-zero start time")

	_, ok = earliestBackupStart([]BackupInfo{{Label: "no-start"}})
	require.False(t, ok, "no usable start time -> not found (preflight fails open)")

	_, ok = earliestBackupStart(nil)
	require.False(t, ok, "no backups -> not found")
}
