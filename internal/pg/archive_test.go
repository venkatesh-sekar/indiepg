package pg

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/indiepg/internal/exec"
)

// Snapshotting postgresql.auto.conf for rollback happens BEFORE any ALTER SYSTEM
// write — the load-bearing ordering invariant for self-healing config. If the
// snapshot fails (e.g. the file is unreadable), EnsureArchiving must fail closed:
// abort with no settings written, so there is nothing to roll back.
func TestEnsureArchiving_SnapshotFailureAbortsBeforeAnyWrite(t *testing.T) {
	r := exec.NewFakeRunner().On("current_setting", exec.FakeResponse{
		Stdout: "off||minimal|off\n", // needs archive_mode + wal_level → needRestart
	})
	r.On("data_directory", exec.FakeResponse{Stdout: "/data"})
	r.On("postgresql.auto.conf", exec.FakeResponse{Err: errors.New("permission denied")})
	m := newManager(r)

	changed, err := m.EnsureArchiving(context.Background(), "main")
	require.Error(t, err)
	require.False(t, changed)

	for _, c := range r.Calls() {
		require.NotContains(t, joinedArgs(c), "ALTER SYSTEM",
			"no setting may be written when the rollback snapshot could not be taken")
		require.NotContains(t, joinedArgs(c), "restart postgresql",
			"Postgres must not be restarted when the snapshot failed")
	}
}

func joinedArgs(c exec.RunSpec) string {
	return c.Name + " " + strings.Join(c.Args, " ")
}

// On a fresh cluster (archive_mode=off, empty archive_command) EnsureArchiving
// must enable archiving and restart Postgres so pgBackRest can back up. Regression
// for "ERROR: [087]: archive_mode must be enabled".
func TestEnsureArchiving_EnablesAndRestarts(t *testing.T) {
	r := exec.NewFakeRunner().On("current_setting", exec.FakeResponse{
		Stdout: "off||replica|off\n", // archive_mode|archive_command|wal_level|wal_compression
	})
	// The restart path snapshots postgresql.auto.conf first (for rollback), which
	// reads the data directory via SHOW data_directory.
	r.On("data_directory", exec.FakeResponse{Stdout: "/var/lib/postgresql/14/main"})
	m := newManager(r)

	changed, err := m.EnsureArchiving(context.Background(), "main")
	require.NoError(t, err)
	require.True(t, changed)

	var altered, restarted, reloaded bool
	for _, c := range r.Calls() {
		j := joinedArgs(c)
		switch {
		case strings.Contains(j, "ALTER SYSTEM SET archive_mode = 'on'"):
			require.Equal(t, "postgres", c.AsUser, "ALTER SYSTEM must run as the postgres superuser")
			altered = true
		case strings.Contains(j, "archive_command = 'pgbackrest --stanza=main archive-push %p'"):
			// archive_command points WAL archiving at pgBackRest for this stanza.
		case strings.Contains(j, "systemctl restart postgresql"):
			restarted = true
		case strings.Contains(j, "pg_reload_conf"):
			reloaded = true
		}
	}
	require.True(t, altered, "archive_mode must be enabled")
	require.True(t, restarted, "a postmaster-only change (archive_mode) requires a restart")
	require.False(t, reloaded, "a restart already applies the change; no reload needed")
}

// When everything is already satisfied, EnsureArchiving is a no-op: no ALTER
// SYSTEM, no restart. archive_mode=always and wal_level=logical are accepted as
// stronger-than-required and never downgraded.
func TestEnsureArchiving_IdempotentNoChange(t *testing.T) {
	r := exec.NewFakeRunner().On("current_setting", exec.FakeResponse{
		Stdout: "always|pgbackrest --stanza=main archive-push %p|logical|on\n",
	})
	m := newManager(r)

	changed, err := m.EnsureArchiving(context.Background(), "main")
	require.NoError(t, err)
	require.False(t, changed)

	for _, c := range r.Calls() {
		j := joinedArgs(c)
		require.NotContains(t, j, "ALTER SYSTEM", "satisfied settings must not be rewritten")
		require.NotContains(t, j, "restart", "no restart when nothing changed")
	}
}

// Only a reloadable setting (archive_command) changed: apply via reload, not a
// full Postgres restart.
func TestEnsureArchiving_ReloadWhenOnlyCommandChanges(t *testing.T) {
	r := exec.NewFakeRunner().On("current_setting", exec.FakeResponse{
		Stdout: "on|/usr/bin/false %p|replica|on\n",
	})
	m := newManager(r)

	changed, err := m.EnsureArchiving(context.Background(), "main")
	require.NoError(t, err)
	require.True(t, changed)

	var restarted, reloaded bool
	for _, c := range r.Calls() {
		j := joinedArgs(c)
		if strings.Contains(j, "systemctl restart") {
			restarted = true
		}
		if strings.Contains(j, "pg_reload_conf") {
			reloaded = true
		}
	}
	require.False(t, restarted, "archive_command is reloadable; no restart required")
	require.True(t, reloaded, "a reloadable-only change must be applied via pg_reload_conf")
}
