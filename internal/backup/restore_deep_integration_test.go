//go:build integration

// End-to-end integration proof that RestoreTestDeep actually restores, boots,
// and counts rows against REAL pgBackRest + Postgres binaries — the one thing
// the fake-Runner unit tests in restore_deep_test.go cannot cover. It stands up
// a throwaway PG cluster with a local pgBackRest stanza, takes a real full
// backup of N seeded rows, then proves the deep restore-test boots a scratch
// copy (full WAL replay) and records a `success` row with VerifiedRows > 0.
//
// Gated behind the "integration" build tag and skipped unless INDIEPG_PG_BINDIR
// points at a Postgres bin directory (e.g. /usr/lib/postgresql/14/bin) with a
// pgbackrest binary on PATH. It NEVER runs in the loop's untagged
// `go test ./...` gate. Run it explicitly:
//
//	INDIEPG_PG_BINDIR=/usr/lib/postgresql/14/bin \
//	  go test -tags integration ./internal/backup/ -run TestRestoreTestDeep_Integration -v
//
// The test runs every binary as the CURRENT OS user (it strips the production
// AsUser="postgres" via stripUserRunner) so no sudo or postgres login is needed;
// it gives the current user a superuser role in the throwaway cluster so
// pgBackRest can connect.
package backup

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/venkatesh-sekar/indiepg/internal/core"
	iexec "github.com/venkatesh-sekar/indiepg/internal/exec"
)

// stripUserRunner wraps a real OSRunner for the integration test: it drops the
// production AsUser="postgres" (so every command runs as the current user, no
// sudo) and layers PGBACKREST_CONFIG so pgBackRest reads the throwaway stanza
// config instead of /etc/pgbackrest. Postgres started via pg_ctl inherits the
// env, so its recovery restore_command (archive-get) finds the repo too.
type stripUserRunner struct {
	inner iexec.Runner
	env   []string
}

func (r stripUserRunner) Run(ctx context.Context, spec iexec.RunSpec) (iexec.RunResult, error) {
	spec.AsUser = ""
	spec.Env = append(append([]string{}, spec.Env...), r.env...)
	return r.inner.Run(ctx, spec)
}

func (r stripUserRunner) DryRun() bool { return r.inner.DryRun() }

func TestRestoreTestDeep_Integration(t *testing.T) {
	binDir := os.Getenv("INDIEPG_PG_BINDIR")
	if binDir == "" {
		t.Skip("set INDIEPG_PG_BINDIR to a Postgres bin dir (e.g. /usr/lib/postgresql/14/bin) to run the deep restore-test integration")
	}
	if _, err := exec.LookPath("pgbackrest"); err != nil {
		t.Skip("pgbackrest not found on PATH; skipping deep restore-test integration")
	}
	for _, b := range []string{"initdb", "pg_ctl", "psql"} {
		if _, err := os.Stat(filepath.Join(binDir, b)); err != nil {
			t.Skipf("%s not found in INDIEPG_PG_BINDIR=%s; skipping", b, binDir)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	base := t.TempDir()
	dataDir := filepath.Join(base, "data")
	repoDir := filepath.Join(base, "repo")
	sockDir := filepath.Join(base, "sock")
	scratchRoot := filepath.Join(base, "scratch")
	confPath := filepath.Join(base, "pgbackrest.conf")
	for _, d := range []string{repoDir, sockDir, scratchRoot,
		filepath.Join(base, "log"), filepath.Join(base, "lock"), filepath.Join(base, "spool")} {
		require.NoError(t, os.MkdirAll(d, 0o700))
	}

	// A runner that runs everything as us and points pgBackRest at our config.
	runner := stripUserRunner{
		inner: iexec.NewOSRunner(core.Discard(), false),
		env:   []string{"PGBACKREST_CONFIG=" + confPath},
	}
	const port = "5499"
	run := func(spec iexec.RunSpec) iexec.RunResult {
		t.Helper()
		res, err := runner.Run(ctx, spec)
		require.NoError(t, err, "command failed: %s %v\nstderr: %s", spec.Name, spec.Args, res.Stderr)
		return res
	}
	bin := func(name string) string { return filepath.Join(binDir, name) }

	// 1. Initialize a throwaway cluster (trust auth so both the current user and
	//    postgres can connect over the socket without a password).
	run(iexec.RunSpec{Name: bin("initdb"), Args: []string{"-A", "trust", "-U", "postgres", "-D", dataDir}, Timeout: 2 * time.Minute})

	// 2. Write the pgBackRest config for a local repo bound to this cluster. The
	//    lock/spool/log paths are private so they never collide with another
	//    user's /tmp/pgbackrest (or a system install).
	conf := fmt.Sprintf(`[global]
repo1-path=%s
repo1-retention-full=1
log-path=%s
lock-path=%s
spool-path=%s
log-level-console=warn
log-level-file=detail
start-fast=y
archive-async=n

[main]
pg1-path=%s
pg1-port=%s
pg1-socket-path=%s
`, repoDir, filepath.Join(base, "log"), filepath.Join(base, "lock"),
		filepath.Join(base, "spool"), dataDir, port, sockDir)
	require.NoError(t, os.WriteFile(confPath, []byte(conf), 0o600))

	// 3. Configure the cluster for archiving into the stanza (private socket, no
	//    TCP listener), then start it.
	pgConf := fmt.Sprintf(`
port=%s
listen_addresses=''
unix_socket_directories='%s'
archive_mode=on
archive_command='pgbackrest --stanza=main archive-push %%p'
wal_level=replica
`, port, sockDir)
	f, err := os.OpenFile(filepath.Join(dataDir, "postgresql.conf"), os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = f.WriteString(pgConf)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	run(iexec.RunSpec{Name: bin("pg_ctl"), Args: []string{"-D", dataDir, "-w", "-t", "60", "start"}, Timeout: 90 * time.Second})
	defer func() {
		// Best-effort teardown of the live cluster (the scratch one is torn down by
		// RestoreTestDeep itself).
		stopCtx, c := context.WithTimeout(context.Background(), 30*time.Second)
		defer c()
		_, _ = runner.Run(stopCtx, iexec.RunSpec{Name: bin("pg_ctl"), Args: []string{"-D", dataDir, "-m", "immediate", "-w", "stop"}, Timeout: 30 * time.Second})
	}()

	// pgBackRest connects to Postgres as the current OS user; give it a login role.
	me, err := user.Current()
	require.NoError(t, err)
	psql := func(sql string) {
		t.Helper()
		run(iexec.RunSpec{Name: bin("psql"), Args: []string{"-h", sockDir, "-p", port, "-U", "postgres", "-d", "postgres", "-v", "ON_ERROR_STOP=1", "-qc", sql}, Timeout: 30 * time.Second})
	}
	psql(fmt.Sprintf(`CREATE ROLE %q SUPERUSER LOGIN`, me.Username))

	// 4. Create the stanza and seed a known number of rows, then ANALYZE so the
	//    reltuples-based deep row count has a non-zero estimate to read back.
	run(iexec.RunSpec{Name: "pgbackrest", Args: []string{"--stanza=main", "stanza-create"}, Timeout: time.Minute})
	run(iexec.RunSpec{Name: "pgbackrest", Args: []string{"--stanza=main", "check"}, Timeout: time.Minute})
	const seededRows = 1234
	psql(fmt.Sprintf("CREATE TABLE durability_probe (id int); INSERT INTO durability_probe SELECT generate_series(1,%d); ANALYZE durability_probe", seededRows))

	// 5. Build the Manager under test against the real binaries. Local-only repo
	//    (no bucket/endpoint) ⇒ a nil Owner is allowed. resolvePGBin is pinned to
	//    the test's bin dir so the test does not depend on the host package layout.
	m := New(Options{
		Runner:      runner,
		Store:       newTestStore(t),
		Config:      testConfig(),
		Logger:      core.Discard(),
		ScratchRoot: scratchRoot,
	})
	m.resolvePGBin = func(string) (string, error) { return binDir, nil }

	// 6. Take a real full backup through the Manager (exercises the real backup
	//    path; the stop-WAL is archived before it returns, so the deep restore has
	//    everything it needs to reach consistency).
	bres, err := m.Backup(ctx, TypeFull)
	require.NoError(t, err)
	require.True(t, bres.OK)

	// 7. The actual subject under test: a real restore + boot + row count.
	res, err := m.RestoreTestDeep(ctx)
	require.NoError(t, err, "deep restore test must pass against real binaries")
	require.True(t, res.OK)
	require.Equal(t, "scratch restore + boot", res.Data["method"])
	rows, _ := res.Data["verified_rows"].(int64)
	require.Greater(t, rows, int64(0), "the booted scratch cluster must report a real (non-zero) row count")

	// 8. A success row with VerifiedRows > 0 was persisted — the durability
	//    surfacing's "proven recoverable" answer is backed by a real recovery.
	recs, err := m.store.ListRestoreTests(ctx, 10)
	require.NoError(t, err)
	require.Len(t, recs, 1)
	require.Equal(t, "success", recs[0].Result)
	require.Greater(t, recs[0].VerifiedRows, int64(0))

	// 9. The deep restore always cleans up after itself: no scratch dir remains.
	entries, err := os.ReadDir(scratchRoot)
	require.NoError(t, err)
	for _, e := range entries {
		require.NotContains(t, e.Name(), "indiepg-restoretest-", "scratch dir was left behind: %s", e.Name())
	}
}
