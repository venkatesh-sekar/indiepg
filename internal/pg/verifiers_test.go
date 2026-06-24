package pg

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/exec"
)

// A representative (well-formed-looking) SCRAM-SHA-256 verifier, as stored in
// pg_authid.rolpassword. Its content is opaque to RoleVerifiers — the SCRAM gate
// lives in pgbouncer.RenderUserlist — so the exact bytes only need to be stable.
const sampleSCRAM = "SCRAM-SHA-256$4096:c2FsdHNhbHQ=$c3RvcmVka2V5:c2VydmVya2V5"

func TestRoleVerifiers_ReadsViaPostgresSuperuser(t *testing.T) {
	other := "SCRAM-SHA-256$4096:b3RoZXJzYWx0$b3RoZXJrZXk=:b3RoZXJzcnY="
	r := exec.NewFakeRunner().On("pg_authid", exec.FakeResponse{
		// psql -tA emits rows already ORDER BY rolname; one row per line.
		Stdout: "app_one|" + sampleSCRAM + "\napp_two|" + other + "\n",
	})
	m := newManager(r)

	got, err := m.RoleVerifiers(context.Background(), []string{"app_two", "app_one"})
	require.NoError(t, err)
	require.Equal(t, []RoleVerifier{
		{Name: "app_one", Verifier: sampleSCRAM},
		{Name: "app_two", Verifier: other},
	}, got, "verifiers returned verbatim, sorted by role name")

	calls := r.Calls()
	require.Len(t, calls, 1)
	require.Equal(t, "psql", calls[0].Name)
	require.Equal(t, "postgres", calls[0].AsUser,
		"pg_authid.rolpassword is superuser-only; must read as the postgres superuser")
	joined := strings.Join(calls[0].Args, " ")
	require.Contains(t, joined, "pg_authid")
	require.Contains(t, joined, "'app_one'")
	require.Contains(t, joined, "'app_two'")
}

func TestRoleVerifiers_PassesNonSCRAMVerifierThrough(t *testing.T) {
	// The SCRAM-vs-md5 decision belongs to RenderUserlist (the single gate), not
	// here: RoleVerifiers must hand back whatever is stored, verbatim, so the
	// downstream gate can refuse it with a precise message.
	r := exec.NewFakeRunner().On("pg_authid", exec.FakeResponse{
		Stdout: "legacy|md5abc123\n",
	})
	m := newManager(r)

	got, err := m.RoleVerifiers(context.Background(), []string{"legacy"})
	require.NoError(t, err)
	require.Equal(t, []RoleVerifier{{Name: "legacy", Verifier: "md5abc123"}}, got)
}

func TestRoleVerifiers_DeduplicatesRequest(t *testing.T) {
	r := exec.NewFakeRunner().On("pg_authid", exec.FakeResponse{
		Stdout: "app|" + sampleSCRAM + "\n",
	})
	m := newManager(r)

	got, err := m.RoleVerifiers(context.Background(), []string{"app", "app"})
	require.NoError(t, err)
	require.Equal(t, []RoleVerifier{{Name: "app", Verifier: sampleSCRAM}}, got)

	// Asking for the same role twice puts it in the IN-list once.
	joined := strings.Join(r.Calls()[0].Args, " ")
	require.Equal(t, 1, strings.Count(joined, "'app'"))
}

func TestRoleVerifiers_MissingRoleErrors(t *testing.T) {
	// pg_authid returns only one of the two requested roles.
	r := exec.NewFakeRunner().On("pg_authid", exec.FakeResponse{
		Stdout: "present|" + sampleSCRAM + "\n",
	})
	m := newManager(r)

	_, err := m.RoleVerifiers(context.Background(), []string{"present", "absent"})
	require.Error(t, err)
	require.Equal(t, core.CodeNotFound, core.CodeOf(err))
	require.Contains(t, err.Error(), "absent")
}

func TestRoleVerifiers_EmptyVerifierErrors(t *testing.T) {
	// Role exists but has no stored password (rolpassword NULL → COALESCE '').
	r := exec.NewFakeRunner().On("pg_authid", exec.FakeResponse{
		Stdout: "nopass|\n",
	})
	m := newManager(r)

	_, err := m.RoleVerifiers(context.Background(), []string{"nopass"})
	require.Error(t, err)
	require.Equal(t, core.CodeValidation, core.CodeOf(err))
	require.Contains(t, err.Error(), "nopass")
}

func TestRoleVerifiers_RejectsInvalidNameBeforeAnyQuery(t *testing.T) {
	for _, bad := range []string{`ro'le`, "ro le", "ro\"le", "ro;le", ""} {
		r := exec.NewFakeRunner().On("pg_authid", exec.FakeResponse{Stdout: "x|" + sampleSCRAM})
		m := newManager(r)

		_, err := m.RoleVerifiers(context.Background(), []string{bad})
		require.Error(t, err, "name %q must be rejected", bad)
		require.Equal(t, core.CodeValidation, core.CodeOf(err))
		require.Zero(t, r.CallCount(), "an invalid role name must be rejected before any psql runs")
	}
}

func TestRoleVerifiers_RequiresRunner(t *testing.T) {
	m := New(Options{})
	_, err := m.RoleVerifiers(context.Background(), []string{"app"})
	require.Error(t, err)
	require.Equal(t, core.CodeInternal, core.CodeOf(err))
}

func TestRoleVerifiers_EmptyRequestErrors(t *testing.T) {
	r := exec.NewFakeRunner()
	m := newManager(r)

	_, err := m.RoleVerifiers(context.Background(), nil)
	require.Error(t, err)
	require.Equal(t, core.CodeValidation, core.CodeOf(err))
	require.Zero(t, r.CallCount())
}
