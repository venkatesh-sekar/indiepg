package pg

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/exec"
)

// mixedRec is the fixed recommendation the ApplyTuning tests size against:
// shared_buffers 1228MB, effective_cache_size 3072MB, work_mem 128MB,
// maintenance_work_mem 512MB, max_connections 179.
func mixedRec() TuningRecommendation { return RecommendTuning(4096, 4, ProfileMixed) }

func psqlIssued(calls []exec.RunSpec, substr string) bool {
	for _, c := range calls {
		if c.Name == "psql" && strings.Contains(strings.Join(c.Args, " "), substr) {
			return true
		}
	}
	return false
}

func restartCount(calls []exec.RunSpec) int {
	n := 0
	for _, c := range calls {
		if c.Name == "systemctl" && len(c.Args) > 0 && c.Args[0] == "restart" {
			n++
		}
	}
	return n
}

// When every managed setting already holds the host-sized value (read back from
// pg_settings in its native unit), ApplyTuning is a no-op: no ALTER SYSTEM, no
// restart, no reload. This is what makes re-provisioning safe.
func TestApplyTuning_NoOpWhenAlreadyApplied(t *testing.T) {
	r := exec.NewFakeRunner()
	// Native Postgres units: shared_buffers/effective_cache_size in 8kB blocks,
	// work_mem/maintenance_work_mem in kB, max_connections unit-less.
	r.On("pg_settings", exec.FakeResponse{Stdout: strings.Join([]string{
		"shared_buffers|157184|8kB",       // 1228MB
		"effective_cache_size|393216|8kB", // 3072MB
		"work_mem|131072|kB",              // 128MB
		"maintenance_work_mem|524288|kB",  // 512MB
		"max_connections|179|",
	}, "\n")})
	m := newManager(r)

	changed, err := m.ApplyTuning(context.Background(), mixedRec())
	require.NoError(t, err)
	require.False(t, changed)

	calls := r.Calls()
	require.False(t, psqlIssued(calls, "ALTER SYSTEM"), "no setting should be written when all already match")
	require.Zero(t, restartCount(calls))
	require.Zero(t, countReloads(calls))
}

// A reloadable-only change (work_mem differs; the restart-requiring settings
// already match) is activated with pg_reload_conf and never restarts Postgres,
// and never snapshots auto.conf.
func TestApplyTuning_ReloadOnlyChange(t *testing.T) {
	r := exec.NewFakeRunner()
	r.On("pg_settings", exec.FakeResponse{Stdout: strings.Join([]string{
		"shared_buffers|157184|8kB",       // matches (restart setting)
		"effective_cache_size|393216|8kB", // matches
		"work_mem|65536|kB",               // 64MB — differs from wanted 128MB
		"maintenance_work_mem|524288|kB",  // matches
		"max_connections|179|",            // matches (restart setting)
	}, "\n")})
	r.On("pg_reload_conf", exec.FakeResponse{})
	m := newManager(r)

	changed, err := m.ApplyTuning(context.Background(), mixedRec())
	require.NoError(t, err)
	require.True(t, changed)

	calls := r.Calls()
	require.True(t, psqlIssued(calls, "ALTER SYSTEM SET work_mem"))
	require.False(t, psqlIssued(calls, "ALTER SYSTEM SET shared_buffers"), "matching restart setting must be left alone")
	require.Equal(t, 1, countReloads(calls), "reloadable-only change reloads")
	require.Zero(t, restartCount(calls), "reloadable-only change must not restart")
	// No restart → no snapshot of auto.conf.
	for _, c := range calls {
		require.NotEqual(t, "cat", c.Name, "reload-only path must not snapshot postgresql.auto.conf")
	}
}

// A restart-requiring change (shared_buffers differs) snapshots auto.conf BEFORE
// writing, applies via ALTER SYSTEM, then restarts — and does not reload.
func TestApplyTuning_RestartRequiringChangeSnapshotsThenRestarts(t *testing.T) {
	r := exec.NewFakeRunner()
	r.On("data_directory", exec.FakeResponse{Stdout: "/var/lib/postgresql/16/main"})
	r.On("postgresql.auto.conf", exec.FakeResponse{Stdout: "# prior good config\n"})
	r.On("pg_settings", exec.FakeResponse{Stdout: strings.Join([]string{
		"shared_buffers|16384|8kB", // 128MB default — differs from wanted 1228MB
		"effective_cache_size|393216|8kB",
		"work_mem|131072|kB",
		"maintenance_work_mem|524288|kB",
		"max_connections|179|",
	}, "\n")})
	m := newManager(r)

	changed, err := m.ApplyTuning(context.Background(), mixedRec())
	require.NoError(t, err)
	require.True(t, changed)

	calls := r.Calls()
	require.True(t, psqlIssued(calls, "ALTER SYSTEM SET shared_buffers"))
	require.Equal(t, 1, restartCount(calls), "restart-requiring change restarts")
	require.Zero(t, countReloads(calls), "restart path replaces the reload")

	// The snapshot (cat) must precede the first ALTER SYSTEM write, so a rollback
	// can restore the pre-change config.
	catIdx, alterIdx := -1, -1
	for i, c := range calls {
		if c.Name == "cat" && catIdx == -1 {
			catIdx = i
		}
		if c.Name == "psql" && strings.Contains(strings.Join(c.Args, " "), "ALTER SYSTEM") && alterIdx == -1 {
			alterIdx = i
		}
	}
	require.NotEqual(t, -1, catIdx, "auto.conf must be snapshotted")
	require.NotEqual(t, -1, alterIdx)
	require.Less(t, catIdx, alterIdx, "snapshot must happen before the change is written")
}

// The common fresh-box case: a restart-requiring setting (shared_buffers) and a
// reloadable one (work_mem) both differ. Both are written, and the change is
// activated by a single restart — not a restart plus a redundant reload.
func TestApplyTuning_RestartAndReloadableChangeTogether(t *testing.T) {
	r := exec.NewFakeRunner()
	r.On("data_directory", exec.FakeResponse{Stdout: "/var/lib/postgresql/16/main"})
	r.On("postgresql.auto.conf", exec.FakeResponse{Stdout: "# prior good config\n"})
	r.On("pg_settings", exec.FakeResponse{Stdout: strings.Join([]string{
		"shared_buffers|16384|8kB", // 128MB — differs (restart setting)
		"effective_cache_size|393216|8kB",
		"work_mem|65536|kB", // 64MB — differs (reloadable)
		"maintenance_work_mem|524288|kB",
		"max_connections|179|",
	}, "\n")})
	m := newManager(r)

	changed, err := m.ApplyTuning(context.Background(), mixedRec())
	require.NoError(t, err)
	require.True(t, changed)

	calls := r.Calls()
	require.True(t, psqlIssued(calls, "ALTER SYSTEM SET shared_buffers"))
	require.True(t, psqlIssued(calls, "ALTER SYSTEM SET work_mem"))
	require.Equal(t, 1, restartCount(calls), "a single restart activates both changes")
	require.Zero(t, countReloads(calls), "the restart replaces the reload — no redundant pg_reload_conf")
}

// If the postmaster rejects the restart-requiring change (Postgres fails to come
// back up), ApplyTuning rolls back to last-known-good and returns CodeSafety with
// Postgres running.
func TestApplyTuning_RollsBackWhenRestartFails(t *testing.T) {
	base := exec.NewFakeRunner()
	base.On("data_directory", exec.FakeResponse{Stdout: "/var/lib/postgresql/16/main"})
	base.On("postgresql.auto.conf", exec.FakeResponse{Stdout: "# prior good config\n"})
	base.On("pg_settings", exec.FakeResponse{Stdout: strings.Join([]string{
		"shared_buffers|16384|8kB", // differs → restart required
		"effective_cache_size|393216|8kB",
		"work_mem|131072|kB",
		"maintenance_work_mem|524288|kB",
		"max_connections|179|",
	}, "\n")})
	r := &flakyRestartRunner{FakeRunner: base, failFirstRestart: true}
	m := newManager(r)

	changed, err := m.ApplyTuning(context.Background(), mixedRec())
	require.Error(t, err)
	require.False(t, changed)
	require.Equal(t, core.CodeSafety, core.CodeOf(err))
	require.Equal(t, 2, r.restartCalls, "a failed restart must be followed by a rollback restart")

	// The prior-good config was restored via tee (as the postgres user).
	var sawRestore bool
	for _, c := range base.Calls() {
		if c.Name == "tee" {
			sawRestore = true
			require.Equal(t, "postgres", c.AsUser)
		}
	}
	require.True(t, sawRestore, "rollback must restore postgresql.auto.conf")
}

// A pg_settings read missing one of the managed settings is a hard error, not a
// silent partial apply.
func TestApplyTuning_MissingSettingErrors(t *testing.T) {
	r := exec.NewFakeRunner()
	r.On("pg_settings", exec.FakeResponse{Stdout: strings.Join([]string{
		"shared_buffers|157184|8kB",
		"effective_cache_size|393216|8kB",
		"work_mem|131072|kB",
		"maintenance_work_mem|524288|kB",
		// max_connections deliberately absent
	}, "\n")})
	m := newManager(r)

	_, err := m.ApplyTuning(context.Background(), mixedRec())
	require.Error(t, err)
	require.Equal(t, core.CodeInternal, core.CodeOf(err))
}

func TestApplyTuning_NoRunner(t *testing.T) {
	m := New(Options{})
	_, err := m.ApplyTuning(context.Background(), mixedRec())
	require.Error(t, err)
	require.Equal(t, core.CodeInternal, core.CodeOf(err))
}

// Memory settings are written as quoted string literals; an integer GUC like
// max_connections must be written unquoted, or ALTER SYSTEM with an E'..' form
// could be rejected.
func TestTunedSetting_AlterValueQuoting(t *testing.T) {
	got := map[string]string{}
	for _, s := range tunedSettings(mixedRec()) {
		got[s.name] = s.alterValue()
	}
	require.Equal(t, "'1228MB'", got["shared_buffers"])
	require.Equal(t, "179", got["max_connections"])
}

// olapRec is the host-sized OLAP recommendation the ApplyProfile tests size
// against (4096MB / 4 CPU). It deliberately differs from mixedRec in
// shared_buffers (1638 vs 1228MB), maintenance_work_mem and max_connections, so a
// test can prove ApplyProfile sized to the profile it was handed rather than a
// hardcoded Mixed.
func olapRec() TuningRecommendation { return RecommendTuning(4096, 4, ProfileOLAP) }

// settingsRows renders a recommendation as the pg_settings text the FakeRunner
// returns, in the native units Postgres emits — 8kB blocks for the buffer
// settings, kB for work_mem/maintenance_work_mem, unit-less for max_connections —
// so the byte-conversion round-trip in readTunableSettings is exercised for real
// rather than a synthetic MB==MB identity.
func settingsRows(rec TuningRecommendation) string {
	return strings.Join([]string{
		fmt.Sprintf("shared_buffers|%d|8kB", rec.SharedBuffersMB*128),
		fmt.Sprintf("effective_cache_size|%d|8kB", rec.EffectiveCacheMB*128),
		fmt.Sprintf("work_mem|%d|kB", rec.WorkMemMB*1024),
		fmt.Sprintf("maintenance_work_mem|%d|kB", rec.MaintenanceWorkMemMB*1024),
		fmt.Sprintf("max_connections|%d|", rec.MaxConnections),
	}, "\n")
}

// ApplyProfile resolves the host-sized recommendation for the requested profile
// and delegates to ApplyTuning: switching to a profile the box already holds is a
// no-op (no ALTER SYSTEM, no restart, no reload), which is what keeps re-applying
// the active profile cheap and safe.
func TestApplyProfile_NoOpWhenProfileAlreadyApplied(t *testing.T) {
	r := exec.NewFakeRunner()
	r.On("pg_settings", exec.FakeResponse{Stdout: settingsRows(olapRec())})
	m := newManager(r)
	pinHostTuning(m, 4096, 4)

	changed, err := m.ApplyProfile(context.Background(), ProfileOLAP)
	require.NoError(t, err)
	require.False(t, changed)

	calls := r.Calls()
	require.False(t, psqlIssued(calls, "ALTER SYSTEM"), "an already-applied profile must write nothing")
	require.Zero(t, restartCount(calls))
	require.Zero(t, countReloads(calls))
}

// ApplyProfile must size against the profile it was handed, not a hardcoded one:
// switching to OLAP while the box runs the Mixed values writes the OLAP-sized
// shared_buffers (1638MB, not Mixed's 1228MB) and restarts. Had it computed Mixed
// this would have been a no-op, so the write + value prove the resolution.
func TestApplyProfile_SizesAgainstRequestedProfile(t *testing.T) {
	r := exec.NewFakeRunner()
	r.On("data_directory", exec.FakeResponse{Stdout: "/var/lib/postgresql/16/main"})
	r.On("postgresql.auto.conf", exec.FakeResponse{Stdout: "# prior good config\n"})
	// The box currently holds the Mixed values; ApplyProfile(OLAP) must change it.
	r.On("pg_settings", exec.FakeResponse{Stdout: settingsRows(mixedRec())})
	m := newManager(r)
	pinHostTuning(m, 4096, 4)

	changed, err := m.ApplyProfile(context.Background(), ProfileOLAP)
	require.NoError(t, err)
	require.True(t, changed)

	calls := r.Calls()
	require.True(t, psqlIssued(calls, "ALTER SYSTEM SET shared_buffers = '1638MB'"),
		"ApplyProfile must write the OLAP-sized shared_buffers, proving it resolved OLAP not Mixed")
	require.Equal(t, 1, restartCount(calls), "resizing shared_buffers/max_connections restarts Postgres")
}

func TestSettingUnitBytes(t *testing.T) {
	tests := []struct {
		unit string
		want int64
		ok   bool
	}{
		{"", 1, true}, // unit-less (max_connections)
		{"B", 1, true},
		{"kB", 1024, true},
		{"MB", 1024 * 1024, true},
		{"GB", 1024 * 1024 * 1024, true},
		{"8kB", 8 * 1024, true},
		{"16kB", 16 * 1024, true},
		{"bogus", 0, false},
		{"0kB", 0, false}, // non-positive multiplier rejected
		{"kib", 0, false},
	}
	for _, tt := range tests {
		got, ok := settingUnitBytes(tt.unit)
		require.Equal(t, tt.ok, ok, "unit %q ok", tt.unit)
		if tt.ok {
			require.Equal(t, tt.want, got, "unit %q factor", tt.unit)
		}
	}
}
