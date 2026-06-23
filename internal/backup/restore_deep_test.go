package backup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/exec"
	"github.com/venkatesh-sekar/indiepg/internal/identity"
)

// newTestManager builds a Manager wired for deep-restore-test unit testing: a
// fake runner, an in-memory store and (by default) an unclaimed-repo owner, a
// scratch root under the test's temp dir, and stubbed disk/binary seams so no
// real volume or Postgres install is required. The newest backup in
// sampleInfoJSON has DatabaseSize 2 MiB.
func newDeepTestManager(t *testing.T, runner *exec.FakeRunner) (*Manager, string) {
	t.Helper()
	st := newTestStore(t)
	owner := identity.NewOwner(testIdentity(), newFakeObjectStore(), core.Discard())
	root := t.TempDir()
	m := New(Options{
		Runner:      runner,
		Store:       st,
		Config:      testConfig(),
		Owner:       owner,
		Logger:      core.Discard(),
		ScratchRoot: root,
	})
	// Stub the real-system seams: plenty of free disk, and a fixed bin dir so we
	// never read a PG_VERSION file the fake runner did not write.
	m.diskFree = func(string) (uint64, error) { return 1 << 40, nil } // 1 TiB
	m.resolvePGBin = func(string) (string, error) { return "/pgbin", nil }
	return m, root
}

// happyRunner returns a fake runner that makes every deep-restore step succeed
// with the given row-count query output. The pg_ctl start and stop steps are
// matched on distinct argv tokens ("start"/"stop") so a test can fail one
// without affecting the other.
func happyRunner(rowCountStdout string) *exec.FakeRunner {
	r := exec.NewFakeRunner()
	r.On("output=json", exec.FakeResponse{Stdout: sampleInfoJSON})
	r.On("--archive-mode=off", exec.FakeResponse{Stdout: "restore ok"})
	r.On("start", exec.FakeResponse{Stdout: "server started"})
	r.On("stop", exec.FakeResponse{Stdout: "server stopped"})
	r.On("/pgbin/psql", exec.FakeResponse{Stdout: rowCountStdout})
	return r
}

func TestRestoreTestDeep_HappyPath(t *testing.T) {
	ctx := context.Background()
	runner := happyRunner("4200\n")
	m, root := newDeepTestManager(t, runner)

	res, err := m.RestoreTestDeep(ctx)
	require.NoError(t, err)
	require.True(t, res.OK)
	require.Equal(t, int64(4200), res.Data["verified_rows"])
	require.Equal(t, "scratch restore + boot", res.Data["method"])

	// A success row with the real row count was recorded.
	recs, err := m.store.ListRestoreTests(ctx, 10)
	require.NoError(t, err)
	require.Len(t, recs, 1)
	require.Equal(t, "success", recs[0].Result)
	require.Equal(t, int64(4200), recs[0].VerifiedRows)
	require.Equal(t, "20260102-030000F_20260102-040000I", recs[0].SourceLabel)

	// Safety invariants over the issued commands. The restore's --pg1-path is the
	// authoritative scratch dir; every other isolation check is compared against
	// that EXACT path, not a loose substring, so the assertions cannot pass by a
	// prefix coincidence.
	var scratch string
	var sawRestore, sawBoot bool
	for _, c := range runner.Calls() {
		if v, ok := argValue(c.Args, "--pg1-path="); ok {
			sawRestore = true
			scratch = v
			require.NotContains(t, c.Args, "--delta")
		}
		// The deep test must NEVER run a backup against the live cluster.
		require.NotContains(t, c.Args, "backup")
	}
	require.True(t, sawRestore, "a restore into the scratch dir must have run")

	// The scratch dir is a fresh subdir under our root — NEVER the live data dir.
	require.NotEmpty(t, scratch)
	require.NotEqual(t, root, scratch)
	require.Equal(t, root, filepath.Dir(scratch))
	require.Contains(t, scratch, "indiepg-restoretest-")

	for _, c := range runner.Calls() {
		if c.Name == "/pgbin/pg_ctl" && containsArg(c.Args, "start") {
			sawBoot = true
			boot := strings.Join(c.Args, " ")
			require.Contains(t, boot, "archive_mode=off")  // never archive into live repo
			require.Contains(t, boot, "listen_addresses=") // no TCP listener
			// Socket confined to the EXACT scratch dir (single-quoted, space-safe).
			require.Contains(t, boot, "unix_socket_directories='"+scratch+"'")
		}
	}
	require.True(t, sawBoot, "the scratch cluster must have been booted")

	// Cleanup always runs: the scratch dir is gone.
	requireNoScratchLeft(t, root)
}

func TestRestoreTestDeep_InsufficientHeadroomRefusesBeforeRestore(t *testing.T) {
	ctx := context.Background()
	runner := happyRunner("1\n")
	m, root := newDeepTestManager(t, runner)
	// Less free than the 2 MiB database × 1.25 headroom needs.
	m.diskFree = func(string) (uint64, error) { return 1024, nil }

	_, err := m.RestoreTestDeep(ctx)
	require.Error(t, err)
	require.Equal(t, core.CodeSafety, core.CodeOf(err))

	// No restore was issued, and nothing was recorded (a disk problem, not
	// evidence the backups are unrecoverable).
	for _, c := range runner.Calls() {
		require.NotContains(t, strings.Join(c.Args, " "), "--archive-mode=off")
	}
	recs, err := m.store.ListRestoreTests(ctx, 10)
	require.NoError(t, err)
	require.Empty(t, recs)
	requireNoScratchLeft(t, root)
}

func TestRestoreTestDeep_BootFailureRecordsFailRow(t *testing.T) {
	ctx := context.Background()
	runner := happyRunner("1\n")
	// ONLY boot (start) fails — exactly the recovery-time failure verify cannot
	// catch. stop stays successful (matched on "stop"), so cleanup runs cleanly
	// and the recorded failure is unambiguously the boot path.
	runner.On("start", exec.FakeResponse{ExitCode: 1, Err: core.ExecError("recovery aborted: WAL gap")})
	m, root := newDeepTestManager(t, runner)

	_, err := m.RestoreTestDeep(ctx)
	require.Error(t, err)
	require.Equal(t, core.CodeExec, core.CodeOf(err))

	recs, err := m.store.ListRestoreTests(ctx, 10)
	require.NoError(t, err)
	require.Len(t, recs, 1)
	require.Equal(t, "fail", recs[0].Result)
	require.Contains(t, recs[0].Detail, "boot")

	// The scratch cluster stop (cleanup) must still have been attempted and
	// succeeded — proving cleanup is independent of the boot failure.
	var sawStop bool
	for _, c := range runner.Calls() {
		if c.Name == "/pgbin/pg_ctl" && containsArg(c.Args, "stop") {
			sawStop = true
		}
	}
	require.True(t, sawStop, "cleanup must attempt to stop the scratch cluster")
	requireNoScratchLeft(t, root)
}

func TestRestoreTestDeep_RestoreFailureRecordsFailRow(t *testing.T) {
	ctx := context.Background()
	runner := happyRunner("1\n")
	runner.On("--archive-mode=off", exec.FakeResponse{ExitCode: 1, Err: core.ExecError("repo unreachable")})
	m, root := newDeepTestManager(t, runner)

	_, err := m.RestoreTestDeep(ctx)
	require.Error(t, err)

	recs, err := m.store.ListRestoreTests(ctx, 10)
	require.NoError(t, err)
	require.Len(t, recs, 1)
	require.Equal(t, "fail", recs[0].Result)
	requireNoScratchLeft(t, root)
}

func TestRestoreTestDeep_ForeignOwnerHardStop(t *testing.T) {
	ctx := context.Background()
	runner := happyRunner("1\n")

	st := newTestStore(t)
	os := newFakeObjectStore()
	os.seedForeignMarker(t, 1) // non-stale foreign owner
	owner := identity.NewOwner(testIdentity(), os, core.Discard())
	root := t.TempDir()
	m := New(Options{Runner: runner, Store: st, Config: testConfig(), Owner: owner, Logger: core.Discard(), ScratchRoot: root})
	m.diskFree = func(string) (uint64, error) { return 1 << 40, nil }

	_, err := m.RestoreTestDeep(ctx)
	require.Error(t, err)
	require.Equal(t, core.CodeOwnership, core.CodeOf(err))
	require.Zero(t, runner.CallCount())

	recs, err := st.ListRestoreTests(ctx, 10)
	require.NoError(t, err)
	require.Empty(t, recs)
}

func TestRestoreTestDeep_NoBackupToTest(t *testing.T) {
	ctx := context.Background()
	runner := exec.NewFakeRunner()
	runner.On("output=json", exec.FakeResponse{Stdout: `[{"name":"main","backup":[]}]`})
	m, root := newDeepTestManager(t, runner)

	_, err := m.RestoreTestDeep(ctx)
	require.Error(t, err)
	require.Equal(t, core.CodeNotFound, core.CodeOf(err))

	// No restore issued; nothing recorded.
	for _, c := range runner.Calls() {
		require.NotContains(t, strings.Join(c.Args, " "), "--archive-mode=off")
	}
	requireNoScratchLeft(t, root)
}

func TestRestoreTestDeep_InvalidStanza(t *testing.T) {
	cfg := testConfig()
	cfg.Stanza = "Invalid Stanza"
	m := New(Options{Runner: exec.NewFakeRunner(), Config: cfg, Logger: core.Discard()})
	_, err := m.RestoreTestDeep(context.Background())
	require.Error(t, err)
	require.Equal(t, core.CodeValidation, core.CodeOf(err))
}

func TestRestoreTestDeep_UnparseableRowCountStillSucceeds(t *testing.T) {
	ctx := context.Background()
	runner := happyRunner("not-a-number\n")
	m, _ := newDeepTestManager(t, runner)

	res, err := m.RestoreTestDeep(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(0), res.Data["verified_rows"])

	recs, err := m.store.ListRestoreTests(ctx, 10)
	require.NoError(t, err)
	require.Len(t, recs, 1)
	require.Equal(t, "success", recs[0].Result)
	require.Zero(t, recs[0].VerifiedRows)
	require.Contains(t, recs[0].Detail, "unparseable")
}

func TestParseRowCount(t *testing.T) {
	cases := []struct {
		in     string
		want   int64
		parsed bool
	}{
		{"4200\n", 4200, true},
		{"  0 ", 0, true},
		{"", 0, false},
		{"abc", 0, false},
		{"-5", 0, false}, // a negative count is nonsense; treat as unparsed
	}
	for _, c := range cases {
		got, parsed := parseRowCount(c.in)
		require.Equal(t, c.want, got, "in=%q", c.in)
		require.Equal(t, c.parsed, parsed, "in=%q", c.in)
	}
}

func TestDefaultDiskFree(t *testing.T) {
	free, err := defaultDiskFree(os.TempDir())
	require.NoError(t, err)
	require.Greater(t, free, uint64(0), "a real temp volume must report some free space")

	_, err = defaultDiskFree(filepath.Join(t.TempDir(), "does-not-exist"))
	require.Error(t, err)
}

func TestDefaultResolvePGBin(t *testing.T) {
	dir := t.TempDir()

	// Missing PG_VERSION is an error (we never guess the binary version).
	_, err := defaultResolvePGBin(dir)
	require.Error(t, err)

	// A real major version resolves to the Debian/Ubuntu package layout.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "PG_VERSION"), []byte("16\n"), 0o600))
	bin, err := defaultResolvePGBin(dir)
	require.NoError(t, err)
	require.Equal(t, "/usr/lib/postgresql/16/bin", bin)

	// Garbage is rejected rather than used to build a bogus path.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "PG_VERSION"), []byte("16; rm -rf /"), 0o600))
	_, err = defaultResolvePGBin(dir)
	require.Error(t, err)
}

func containsArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// argValue returns the value of the first argv element with the given prefix
// (e.g. "--pg1-path=/scratch" => "/scratch" for prefix "--pg1-path=").
func argValue(args []string, prefix string) (string, bool) {
	for _, a := range args {
		if strings.HasPrefix(a, prefix) {
			return strings.TrimPrefix(a, prefix), true
		}
	}
	return "", false
}

func requireNoScratchLeft(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	for _, e := range entries {
		require.NotContains(t, e.Name(), "indiepg-restoretest-", "scratch dir was not cleaned up: %s", e.Name())
	}
}
