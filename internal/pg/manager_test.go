package pg

import (
	"context"
	"fmt"
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

// pinTuningNoOp makes a manager's Provision apply a fixed, host-independent
// recommendation and programs the FakeRunner's pg_settings read to report every
// managed setting already at that value (rendered in MB) — so ApplyTuning sees no
// change and Provision's restart/reload behaviour stays deterministic regardless
// of the test host's RAM/CPU.
func pinTuningNoOp(m *Manager, r *exec.FakeRunner) {
	rec := RecommendTuning(4096, 4, ProfileMixed)
	m.detectTuning = func(WorkloadProfile) (TuningRecommendation, bool) { return rec, true }
	// Report each setting in the unit Postgres natively emits — 8kB blocks for
	// shared_buffers/effective_cache_size, kB for work_mem/maintenance_work_mem,
	// unit-less for max_connections — so the no-op detection exercises the real
	// byte-conversion round-trip rather than a synthetic MB==MB identity.
	rows := strings.Join([]string{
		fmt.Sprintf("shared_buffers|%d|8kB", rec.SharedBuffersMB*128),
		fmt.Sprintf("effective_cache_size|%d|8kB", rec.EffectiveCacheMB*128),
		fmt.Sprintf("work_mem|%d|kB", rec.WorkMemMB*1024),
		fmt.Sprintf("maintenance_work_mem|%d|kB", rec.MaintenanceWorkMemMB*1024),
		fmt.Sprintf("max_connections|%d|", rec.MaxConnections),
	}, "\n")
	r.On("pg_settings", exec.FakeResponse{Stdout: rows})
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
	pinTuningNoOp(m, r)

	res, err := m.Provision(context.Background(), ProfileMixed)
	require.NoError(t, err)
	require.True(t, res.OK)
	require.NotEmpty(t, res.Statements)
	require.Equal(t, "already-applied", res.Data["tuning"])
	require.Contains(t, strings.Join(res.Statements, "\n"), "host-sized tuning")

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
	// Versioned PGDG install (default catalog major) replaces the generic
	// `postgresql` metapackage. The contrib modules ship bundled in the server
	// package on Debian/Ubuntu, so no separate (and non-existent) -contrib package
	// is requested — a literal "postgresql-<major>-contrib" would make apt abort.
	require.Contains(t, all, fmt.Sprintf("apt-get install -y postgresql-%d pgbackrest", DefaultMajor()))
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

// TestProvision_SecondRunIsIdempotentNoOp proves re-running Provision on an
// already-provisioned box is safe: it succeeds, does not duplicate the managed
// pg_hba.conf block (the one step that mutates on-disk state), skips the reload
// when nothing changed, and reports the socket-auth step as "already-present"
// rather than claiming it re-did the work.
func TestProvision_SecondRunIsIdempotentNoOp(t *testing.T) {
	r := exec.NewFakeRunner()

	// A single persistent hba file shared across both runs, so the second run
	// sees the managed block the first run wrote — the real idempotency check.
	hbaFile := filepath.Join(t.TempDir(), "pg_hba.conf")
	require.NoError(t, os.WriteFile(hbaFile, []byte("local   all   all   peer\n"), 0o640))
	r.On("hba_file", exec.FakeResponse{Stdout: hbaFile})
	r.On("pg_reload_conf", exec.FakeResponse{})

	m := newManager(r)
	// Tuning is already at the host-sized value on both runs, so it never restarts
	// or reloads — keeping the reload-count invariant below about pg_hba alone.
	pinTuningNoOp(m, r)

	// First provision: fresh box. Socket auth is configured and the reload fires.
	first, err := m.Provision(context.Background(), ProfileMixed)
	require.NoError(t, err)
	require.True(t, first.OK)
	require.Equal(t, "configured", first.Data["socket_auth"])
	require.Contains(t, strings.Join(first.Statements, "\n"), "configured pg_hba.conf socket auth")

	reloadsAfterFirst := countReloads(r.Calls())
	require.Equal(t, 1, reloadsAfterFirst, "first run reloads after writing the managed block")

	// Second provision: already-provisioned box. It must succeed and be a no-op
	// for the stateful step.
	second, err := m.Provision(context.Background(), ProfileMixed)
	require.NoError(t, err)
	require.True(t, second.OK)
	require.Equal(t, "already-present", second.Data["socket_auth"])
	require.NotContains(t, strings.Join(second.Statements, "\n"), "configured pg_hba.conf socket auth",
		"a re-run must not claim it re-configured socket auth")

	// The managed block exists exactly once — the second run did not stack a
	// duplicate (which would corrupt pg_hba.conf authentication).
	hbaContent, err := os.ReadFile(hbaFile)
	require.NoError(t, err)
	require.Equal(t, 1, strings.Count(string(hbaContent), "indiepg managed (socket auth"),
		"the managed block must appear exactly once after two provisions")

	// No extra reload was issued on the no-op run.
	require.Equal(t, reloadsAfterFirst, countReloads(r.Calls()),
		"a no-op re-run must not reload Postgres config")
}

// countReloads counts pg_reload_conf invocations in the recorded calls.
func countReloads(calls []exec.RunSpec) int {
	n := 0
	for _, c := range calls {
		if strings.Contains(strings.Join(c.Args, " "), "pg_reload_conf") {
			n++
		}
	}
	return n
}

func TestProvision_AptInstallFailureStops(t *testing.T) {
	r := exec.NewFakeRunner()
	r.On("install", exec.FakeResponse{ExitCode: 100, Err: errFake("dpkg lock")})
	m := newManager(r)

	_, err := m.Provision(context.Background(), ProfileMixed)
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
	_, err := m.Provision(context.Background(), ProfileMixed)
	require.Error(t, err)
	require.Equal(t, core.CodeInternal, core.CodeOf(err))
}

// TestProvision_AppliesGivenProfileNotHardcodedMixed proves Provision sizes
// Postgres to the profile it is HANDED, not a hardcoded Mixed: re-running install
// after an operator picked OLAP must re-apply OLAP. We pin a fixed host (so each
// profile yields a genuinely distinct recommendation) and program pg_settings to
// already hold the OLAP-sized values, making Provision(ctx, ProfileOLAP) a tuning
// no-op ("already-applied"). Had Provision used Mixed — whose shared_buffers /
// max_connections differ — it would instead have issued ALTER SYSTEM and restarted
// Postgres, so the no-op is the proof it resolved OLAP.
func TestProvision_AppliesGivenProfileNotHardcodedMixed(t *testing.T) {
	r := exec.NewFakeRunner()
	hbaFile := filepath.Join(t.TempDir(), "pg_hba.conf")
	require.NoError(t, os.WriteFile(hbaFile, []byte("local   all   all   peer\n"), 0o640))
	r.On("hba_file", exec.FakeResponse{Stdout: hbaFile})
	r.On("pg_reload_conf", exec.FakeResponse{})

	m := newManager(r)
	pinHostTuning(m, 4096, 4)
	// The box already holds the OLAP recommendation (reported in native units).
	r.On("pg_settings", exec.FakeResponse{Stdout: settingsRows(RecommendTuning(4096, 4, ProfileOLAP))})

	res, err := m.Provision(context.Background(), ProfileOLAP)
	require.NoError(t, err)
	require.True(t, res.OK)
	require.Equal(t, "already-applied", res.Data["tuning"],
		"Provision(OLAP) against an OLAP-tuned box must be a tuning no-op; a hardcoded Mixed would have re-tuned")
	require.False(t, psqlIssued(r.Calls(), "ALTER SYSTEM"),
		"Provision must not ALTER SYSTEM when the box already holds the requested profile's values")
	require.Zero(t, restartCount(r.Calls()),
		"a no-op tuning re-provision must not restart Postgres")
}

// IsRunning's running/down/no-runner contract is exercised in
// is_running_test.go (TestIsRunning_ProbesPostmasterNotSystemdWrapper), which
// proves it keys off a real SELECT 1 liveness probe rather than the lying
// systemd wrapper. This case pins the specific error code for the
// runner-misconfigured path.
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
