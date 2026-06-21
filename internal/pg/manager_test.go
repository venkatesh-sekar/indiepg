package pg

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/indiepg/internal/config"
	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/exec"
)

func newManager(r exec.Runner) *Manager {
	return New(Options{Runner: r, Config: config.Default()})
}

// joinedCall renders a recorded RunSpec the way the FakeRunner matches it, so
// tests can assert on the resolved argv.
func joinedCall(spec exec.RunSpec) string {
	parts := make([]string, 0, len(spec.Args)+4)
	if spec.AsUser != "" {
		parts = append(parts, "sudo", "-u", spec.AsUser)
	}
	parts = append(parts, spec.Name)
	parts = append(parts, spec.Args...)
	return strings.Join(parts, " ")
}

func TestProvision_HappyPath(t *testing.T) {
	r := exec.NewFakeRunner()
	r.On("system_identifier", exec.FakeResponse{}) // unused here

	// Provision configures pg_hba.conf socket auth for the panel roles: it asks
	// Postgres for the hba file path, edits it, and reloads. Point the fake at a
	// real temp file so ensureSocketAuth can do its (idempotent) edit.
	hbaFile := filepath.Join(t.TempDir(), "pg_hba.conf")
	require.NoError(t, os.WriteFile(hbaFile, []byte("local   all   all   peer\n"), 0o640))
	r.On("hba_file", exec.FakeResponse{Stdout: hbaFile})
	r.On("pg_reload_conf", exec.FakeResponse{})

	m := newManager(r)

	res, err := m.Provision(context.Background())
	require.NoError(t, err)
	require.True(t, res.OK)
	require.NotEmpty(t, res.Statements)

	// The managed trust block must have been written ahead of the default rule.
	hbaContent, err := os.ReadFile(hbaFile)
	require.NoError(t, err)
	require.Contains(t, string(hbaContent), "indiepg managed")
	require.Contains(t, string(hbaContent), ReadOnlyRole+"   trust")
	require.Contains(t, string(hbaContent), AdminRole+"   trust")
	require.True(t, strings.Index(string(hbaContent), "indiepg managed") <
		strings.Index(string(hbaContent), "local   all   all   peer"),
		"managed block must precede the default peer rule")

	calls := r.Calls()
	require.NotEmpty(t, calls)

	joined := make([]string, len(calls))
	for i, c := range calls {
		joined[i] = joinedCall(c)
	}
	all := strings.Join(joined, "\n")

	require.Contains(t, all, "apt-get update")
	require.Contains(t, all, "apt-get install -y postgresql postgresql-contrib")
	require.Contains(t, all, "systemctl enable --now postgresql")
	// roles + extension are created via psql run as the postgres OS user.
	require.Contains(t, all, "sudo -u postgres psql")
	require.Contains(t, all, "pg_stat_statements")

	// every psql admin step targets the maintenance database and stops on error.
	for _, c := range calls {
		if c.Name == "psql" {
			require.Equal(t, "postgres", c.AsUser)
			require.Contains(t, c.Args, "ON_ERROR_STOP=1")
		}
	}
}

func TestProvision_AptInstallFailureStops(t *testing.T) {
	r := exec.NewFakeRunner()
	r.On("install", exec.FakeResponse{ExitCode: 100, Err: errFake("dpkg lock")})
	m := newManager(r)

	_, err := m.Provision(context.Background())
	require.Error(t, err)
	require.Equal(t, core.CodeExec, core.CodeOf(err))

	// it must not proceed to systemctl/psql after the install failure.
	for _, c := range r.Calls() {
		require.NotEqual(t, "systemctl", c.Name)
		require.NotEqual(t, "psql", c.Name)
	}
}

func TestProvision_NoRunner(t *testing.T) {
	m := New(Options{Config: config.Default()})
	_, err := m.Provision(context.Background())
	require.Error(t, err)
	require.Equal(t, core.CodeInternal, core.CodeOf(err))
}

func TestIsRunning(t *testing.T) {
	tests := []struct {
		name   string
		stdout string
		resp   exec.FakeResponse
		want   bool
	}{
		{"active", "active\n", exec.FakeResponse{Stdout: "active\n"}, true},
		{"inactive", "inactive\n", exec.FakeResponse{Stdout: "inactive\n", ExitCode: 3, Err: errFake("inactive")}, false},
		{"failed", "failed\n", exec.FakeResponse{Stdout: "failed\n", ExitCode: 3, Err: errFake("failed")}, false},
		{"unknown empty", "", exec.FakeResponse{ExitCode: 4, Err: errFake("unknown")}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := exec.NewFakeRunner()
			r.On("is-active", tt.resp)
			m := newManager(r)
			got, err := m.IsRunning(context.Background())
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestIsRunning_NoRunner(t *testing.T) {
	m := New(Options{Config: config.Default()})
	_, err := m.IsRunning(context.Background())
	require.Error(t, err)
	require.Equal(t, core.CodeInternal, core.CodeOf(err))
}

func TestSystemIdentifier_ViaPsql(t *testing.T) {
	r := exec.NewFakeRunner()
	r.On("pg_control_system", exec.FakeResponse{Stdout: "7361234567890123456\n"})
	m := newManager(r)

	id, err := m.SystemIdentifier(context.Background())
	require.NoError(t, err)
	require.Equal(t, "7361234567890123456", id)

	// run as the postgres OS user.
	var sawPsql bool
	for _, c := range r.Calls() {
		if c.Name == "psql" {
			sawPsql = true
			require.Equal(t, "postgres", c.AsUser)
		}
	}
	require.True(t, sawPsql)
}

func TestSystemIdentifier_EmptyOutput(t *testing.T) {
	r := exec.NewFakeRunner()
	r.On("pg_control_system", exec.FakeResponse{Stdout: "   \n"})
	m := newManager(r)

	_, err := m.SystemIdentifier(context.Background())
	require.Error(t, err)
	require.Equal(t, core.CodeInternal, core.CodeOf(err))
}

func TestSystemIdentifier_PsqlFailure(t *testing.T) {
	r := exec.NewFakeRunner()
	r.On("pg_control_system", exec.FakeResponse{ExitCode: 2, Err: errFake("connection refused")})
	m := newManager(r)

	_, err := m.SystemIdentifier(context.Background())
	require.Error(t, err)
	require.Equal(t, core.CodeExec, core.CodeOf(err))
}

func TestRedactPasswordLiteral(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			"create role",
			`CREATE ROLE "app" LOGIN PASSWORD 's3cr3t';`,
			`CREATE ROLE "app" LOGIN PASSWORD <redacted>;`,
		},
		{
			"alter role",
			`ALTER ROLE "app" PASSWORD 'newpw';`,
			`ALTER ROLE "app" PASSWORD <redacted>;`,
		},
		{
			"lowercase keyword",
			`create role "app" login password 'pw';`,
			`create role "app" login password <redacted>;`,
		},
		{
			"doubled-quote escape in password",
			`CREATE ROLE "app" LOGIN PASSWORD 'p''; DROP TABLE x; --';`,
			`CREATE ROLE "app" LOGIN PASSWORD <redacted>;`,
		},
		{
			"E-prefixed escape string",
			`CREATE ROLE "app" LOGIN PASSWORD E'pa\ss';`,
			`CREATE ROLE "app" LOGIN PASSWORD <redacted>;`,
		},
		{
			"no password literal is untouched",
			`CREATE EXTENSION IF NOT EXISTS pg_stat_statements;`,
			`CREATE EXTENSION IF NOT EXISTS pg_stat_statements;`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, redactPasswordLiteral(tt.in))
			// The redacted form must never retain the original secret value.
			require.NotContains(t, redactPasswordLiteral(tt.in), "s3cr3t")
		})
	}
}

func TestStatementHasSecret(t *testing.T) {
	require.True(t, statementHasSecret(`CREATE ROLE "app" LOGIN PASSWORD 'pw';`))
	require.True(t, statementHasSecret(`ALTER ROLE "app" PASSWORD E'p\w';`))
	require.False(t, statementHasSecret(`CREATE EXTENSION IF NOT EXISTS pg_stat_statements;`))
	require.False(t, statementHasSecret(`SELECT system_identifier::text FROM pg_control_system()`))
}

// TestRunPsql_MarksSensitiveOnPassword verifies that runPsql flags the exec spec
// Sensitive when (and only when) the SQL carries a PASSWORD literal, so the
// Runner redacts the resolved argv instead of logging a cleartext secret.
func TestRunPsql_MarksSensitiveOnPassword(t *testing.T) {
	r := exec.NewFakeRunner()
	m := newManager(r)

	_, err := m.runPsql(context.Background(), "postgres", `CREATE ROLE "app" LOGIN PASSWORD 'topsecret';`)
	require.NoError(t, err)
	_, err = m.runPsql(context.Background(), "postgres", `SELECT 1;`)
	require.NoError(t, err)

	calls := r.Calls()
	require.Len(t, calls, 2)
	require.True(t, calls[0].Sensitive, "statement with a PASSWORD literal must be Sensitive")
	require.False(t, calls[1].Sensitive, "statement without a secret must not be Sensitive")
}

// TestRunPsql_RedactsPasswordInStderr ensures a psql failure does not leak a
// PASSWORD literal that psql echoed back in its error text. The stderr is
// carried as a structured, serializable error detail (it can reach the audit
// log), so the redaction must land there.
func TestRunPsql_RedactsPasswordInStderr(t *testing.T) {
	r := exec.NewFakeRunner()
	r.On("psql", exec.FakeResponse{
		ExitCode: 1,
		Stderr:   `ERROR near CREATE ROLE "app" LOGIN PASSWORD 'topsecret'`,
		Err:      errFake("syntax error"),
	})
	m := newManager(r)

	_, err := m.runPsql(context.Background(), "postgres", `CREATE ROLE "app" LOGIN PASSWORD 'topsecret';`)
	require.Error(t, err)

	pe, ok := core.AsError(err)
	require.True(t, ok)
	stderr, _ := pe.Details["stderr"].(string)
	require.NotContains(t, stderr, "topsecret")
	require.Contains(t, stderr, "PASSWORD <redacted>")
}

// errFake is a tiny error helper for FakeResponse.Err.
type fakeErr string

func (e fakeErr) Error() string { return string(e) }
func errFake(s string) error    { return fakeErr(s) }
