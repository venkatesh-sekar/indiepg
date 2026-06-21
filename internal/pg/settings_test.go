package pg

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/exec"
)

// data_directory is a superuser-only GUC the panel's non-superuser pool role
// cannot read, so DataDirectory must read it via psql as the postgres OS user
// (peer-auth superuser). Regression for "server: discover Postgres data directory
// for backup config" failing on a local backup.
func TestDataDirectory_ReadsViaPostgresSuperuser(t *testing.T) {
	r := exec.NewFakeRunner().On("data_directory", exec.FakeResponse{
		Stdout: "/var/lib/postgresql/16/main\n",
	})
	m := newManager(r)

	got, err := m.DataDirectory(context.Background())
	require.NoError(t, err)
	require.Equal(t, "/var/lib/postgresql/16/main", got)

	calls := r.Calls()
	require.Len(t, calls, 1)
	require.Equal(t, "psql", calls[0].Name)
	require.Equal(t, "postgres", calls[0].AsUser,
		"a superuser-only GUC must be read as the postgres superuser")
}

func TestDataDirectory_EmptyOutputErrors(t *testing.T) {
	r := exec.NewFakeRunner().On("data_directory", exec.FakeResponse{Stdout: "  \n"})
	m := newManager(r)

	_, err := m.DataDirectory(context.Background())
	require.Error(t, err)
	require.Equal(t, core.CodeInternal, core.CodeOf(err))
}
