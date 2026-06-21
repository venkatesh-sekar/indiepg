package pg

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/indiepg/internal/exec"
)

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
