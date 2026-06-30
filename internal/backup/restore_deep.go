package backup

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/exec"
	"github.com/venkatesh-sekar/indiepg/internal/store"
)

// Deep restore-test tuning.
const (
	// deepHeadroomFactor scales the (uncompressed) database size to the free
	// space the scratch volume must have before a deep restore is allowed to run.
	// A restore writes up to the full database size into the scratch directory;
	// requiring a margin above that keeps the test from ever filling the volume
	// — which, on a volume shared with the live data dir, could itself cause data
	// loss. This is the safety precheck the cheap `verify` path never needed.
	deepHeadroomFactor = 1.25

	// deepRestorePort names the scratch cluster's private unix socket inside the
	// scratch dir. listen_addresses is empty so there is no TCP listener and thus
	// no way to collide with the live cluster's port; the socket lives in the
	// scratch dir, so even the socket path cannot collide with the live one.
	deepRestorePort = "5499"

	// deepBootTimeout bounds how long we wait for the restored scratch cluster to
	// finish WAL replay and accept connections.
	deepBootTimeout = 10 * time.Minute
)

// defaultDiskFree reports the bytes available to a non-root user on the
// filesystem backing path.
func defaultDiskFree(path string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, core.InternalError("statfs %q for restore-test headroom check", path).Wrap(err)
	}
	// Bavail is blocks available to an unprivileged user; Bsize is the block size.
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}

// defaultResolvePGBin reads the restored cluster's PG_VERSION and returns the
// directory holding the matching pg_ctl/psql for the Debian/Ubuntu package
// layout the panel installs into (/usr/lib/postgresql/<major>/bin). The binary
// MUST match the cluster's catalog version, so it is derived from the restored
// data dir rather than assumed.
func defaultResolvePGBin(dataDir string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(dataDir, "PG_VERSION"))
	if err != nil {
		return "", core.InternalError("read PG_VERSION from restored scratch cluster").Wrap(err)
	}
	major := strings.TrimSpace(string(raw))
	if major == "" {
		return "", core.InternalError("empty PG_VERSION in restored scratch cluster")
	}
	for _, r := range major {
		if (r < '0' || r > '9') && r != '.' { // "14"/"16", or pre-10 "9.6"
			return "", core.InternalError("unexpected PG_VERSION %q in restored scratch cluster", major)
		}
	}
	return "/usr/lib/postgresql/" + major + "/bin", nil
}

// DeepRestoreCmd builds a `pgbackrest restore` that targets an isolated scratch
// data directory (--pg1-path overrides the configured live path) and disables
// archiving in the restored config (--archive-mode=off) so the booted scratch
// cluster can NEVER push WAL into the live repository. No --delta and no
// --type=none: the scratch dir is empty and we WANT full WAL replay, since that
// is exactly the recovery-time failure (a WAL gap, a corrupt pg_control) that
// the cheap `verify` check cannot detect.
func DeepRestoreCmd(stanza, pg1Path string) exec.RunSpec {
	return exec.RunSpec{
		Name: pgbackrestBin,
		Args: []string{
			"--stanza=" + stanza,
			"--pg1-path=" + pg1Path,
			"--archive-mode=off",
			"restore",
		},
		AsUser:  pgUser,
		Timeout: restoreTimeout,
	}
}

// scratchStartLog is the file pg_ctl redirects the booted scratch server's
// stdout/stderr into (via -l), inside the scratch dir so it is cleaned up with it.
const scratchStartLog = "pg_ctl_start.log"

// pgCtlStartCmd boots the restored scratch cluster on a private unix socket with
// no TCP listener and archiving off, so it cannot collide with — or write into —
// the live cluster or repository. pg_ctl -w waits until recovery completes and
// the server accepts connections (or the timeout trips).
//
// -l redirects the booted server's stdout/stderr into a logfile in the scratch
// dir. This is REQUIRED, not cosmetic: without it the long-lived postmaster
// inherits the command runner's stdout/stderr pipe and keeps it open after pg_ctl
// itself exits, so the runner's cmd.Wait() would block reading that pipe until the
// scratch cluster is later torn down — wedging the whole deep restore-test. With
// -l the server writes to the file instead, so the pipe closes when pg_ctl returns
// and the runner advances to the row-count step immediately.
func pgCtlStartCmd(binDir, dataDir string) exec.RunSpec {
	// pg_ctl re-shells the -o options string when it invokes postgres, so the
	// socket-directory path is single-quoted to survive a scratch root that
	// contains spaces (an operator-configured ScratchRoot might).
	opts := strings.Join([]string{
		"-p " + deepRestorePort,
		"-c listen_addresses=", // empty => no TCP listener
		"-c unix_socket_directories='" + dataDir + "'",
		"-c archive_mode=off", // belt-and-suspenders with --archive-mode=off above
	}, " ")
	return exec.RunSpec{
		Name: filepath.Join(binDir, "pg_ctl"),
		Args: []string{
			"-D", dataDir,
			"-l", filepath.Join(dataDir, scratchStartLog),
			"-w",
			"-t", strconv.Itoa(int(deepBootTimeout / time.Second)),
			"-o", opts,
			"start",
		},
		AsUser:  pgUser,
		Timeout: deepBootTimeout + time.Minute,
	}
}

// pgCtlStopCmd stops the scratch cluster immediately (it holds no data we need
// to flush). It is best-effort cleanup; a non-running cluster is fine.
func pgCtlStopCmd(binDir, dataDir string) exec.RunSpec {
	return exec.RunSpec{
		Name:    filepath.Join(binDir, "pg_ctl"),
		Args:    []string{"-D", dataDir, "-m", "immediate", "-w", "stop"},
		AsUser:  pgUser,
		Timeout: time.Minute,
	}
}

// deepRowCountCmd queries the booted scratch cluster for an estimated user-table
// row count over the private socket. A successful query proves the cluster
// booted, the catalog is readable, and the heap is queryable; the count is
// recorded as corroborating evidence (it is reltuples-based, hence approximate).
func deepRowCountCmd(binDir, dataDir string) exec.RunSpec {
	const q = `SELECT coalesce(sum(greatest(reltuples,0)),0)::bigint ` +
		`FROM pg_class WHERE relkind IN ('r','p') ` +
		`AND relnamespace NOT IN ('pg_catalog'::regnamespace,'information_schema'::regnamespace);`
	return exec.RunSpec{
		Name: filepath.Join(binDir, "psql"),
		Args: []string{
			"-h", dataDir,
			"-p", deepRestorePort,
			"-U", pgUser,
			"-d", "postgres",
			"-At",
			"-c", q,
		},
		AsUser:  pgUser,
		Timeout: infoTimeout,
	}
}

// RestoreTestDeep proves a backup is actually recoverable by performing a real,
// NON-DESTRUCTIVE restore into a throwaway scratch cluster, booting it (full WAL
// replay), and counting rows — then tearing it down. Unlike the cheap `verify`
// (RestoreTest), this catches recovery-time failures that repo-checksums cannot:
// a WAL-replay gap, a corrupt pg_control, an unbootable catalog.
//
// It is the heaviest, opt-in durability drill and is guarded accordingly:
//
//   - Foreign-owned repo  => HARD STOP before anything runs (read-side ownership).
//   - Disk-headroom gate  => refuses (no restore issued) unless the scratch volume
//     has deepHeadroomFactor× the database size free, so a restore can never fill
//     the box and threaten the live data dir.
//   - Isolation           => restores to a fresh scratch dir (never the live data
//     dir), with archiving OFF and a private socket, so it can never touch the
//     live cluster or write into the repository.
//   - Guaranteed cleanup  => the scratch cluster is always stopped and the scratch
//     dir always removed, even on error or panic.
//
// The pass/fail outcome (and the verified row count) is recorded in
// store.restore_tests on a detached context so a shutdown never drops the record.
func (m *Manager) RestoreTestDeep(ctx context.Context) (core.Result, error) {
	stanza := m.config().Stanza
	if err := validateStanza(stanza); err != nil {
		return core.Result{}, err
	}

	// Reading a foreign-owned repo is a HARD STOP (mirrors Restore's read side).
	if err := m.verifyForRead(ctx); err != nil {
		return core.Result{}, err
	}

	// Size the restore from the newest backup and gate on disk headroom BEFORE
	// writing anything. A precondition failure here records no restore_tests row:
	// it is a disk/operator problem, not evidence the backups are unrecoverable.
	info, err := m.Info(ctx)
	if err != nil {
		return core.Result{}, err
	}
	if len(info) == 0 {
		return core.Result{}, core.NotFoundError("no backup exists to deep-restore-test for stanza %q", stanza)
	}
	sourceLabel := info[0].Label
	dbSize := info[0].DatabaseSize
	if dbSize <= 0 {
		return core.Result{}, core.NewSafetyError(
			"deep restore test (stanza "+stanza+")",
			[]string{"a known database size to size the disk-headroom check"},
			"refusing to run a deep restore test: cannot determine the database size from backup %q, so the disk-headroom safety check is impossible; the live cluster was not touched",
			sourceLabel,
		)
	}
	need := uint64(float64(dbSize) * deepHeadroomFactor)
	// Ensure the scratch root exists before measuring/using it, so an operator who
	// points ScratchRoot at a not-yet-created path gets a working test (and a clear
	// error if the path is unusable) rather than an opaque statfs failure.
	if err := os.MkdirAll(m.scratchRoot, 0o700); err != nil {
		return core.Result{}, core.InternalError("prepare restore-test scratch root %q", m.scratchRoot).Wrap(err)
	}
	free, err := m.diskFree(m.scratchRoot)
	if err != nil {
		return core.Result{}, err
	}
	if free < need {
		return core.Result{}, core.NewSafetyError(
			"deep restore test (stanza "+stanza+")",
			[]string{"sufficient free disk on the scratch volume"},
			"refusing to run a deep restore test: scratch volume %q has %d bytes free but the restore needs ~%d (database %d × %.2f headroom); free space or point ScratchRoot at a roomier volume — the live cluster was not touched",
			m.scratchRoot, free, need, dbSize, deepHeadroomFactor,
		)
	}

	// Fresh, unique scratch dir — never the live data dir. Guaranteed cleanup.
	scratch, err := os.MkdirTemp(m.scratchRoot, "indiepg-restoretest-")
	if err != nil {
		return core.Result{}, core.InternalError("create restore-test scratch dir under %q", m.scratchRoot).Wrap(err)
	}
	// pgBackRest runs as the postgres user and must own the scratch dir to write
	// into it. Best-effort (needs root); harmless when already same-user.
	m.chownScratch(scratch)

	var binDir string
	defer m.cleanupScratch(ctx, &binDir, scratch)

	started := time.Now().UTC()

	// 1. Restore the newest backup into the scratch dir.
	if _, err := m.runner.Run(ctx, DeepRestoreCmd(stanza, scratch)); err != nil {
		return core.Result{}, m.recordDeepFail(ctx, started, sourceLabel, "restore into scratch dir failed: "+err.Error(), err)
	}

	// 2. Resolve the matching Postgres binaries from the restored cluster version.
	binDir, err = m.resolvePGBin(scratch)
	if err != nil {
		return core.Result{}, m.recordDeepFail(ctx, started, sourceLabel, "could not resolve Postgres binaries for the restored cluster: "+err.Error(), err)
	}

	// 2b. Materialize the postgresql.conf/pg_hba.conf/pg_ident.conf the cluster
	//     needs to boot. On Debian these live under /etc (outside PGDATA) so the
	//     PGDATA-only restore never captured them; without this pg_ctl dies with
	//     "could not access the server configuration file".
	if err := m.writeScratchClusterConfig(scratch); err != nil {
		return core.Result{}, m.recordDeepFail(ctx, started, sourceLabel, "could not materialize scratch cluster config before boot: "+err.Error(), err)
	}

	// 3. Boot the scratch cluster — full WAL replay. A failure here is exactly the
	//    recovery-time failure that proves the backup is NOT cleanly recoverable.
	if _, err := m.runner.Run(ctx, pgCtlStartCmd(binDir, scratch)); err != nil {
		return core.Result{}, m.recordDeepFail(ctx, started, sourceLabel, "restored scratch cluster failed to boot (recovery-time failure): "+err.Error(), err)
	}

	// 4. Prove the catalog/heap is queryable and capture a real row count.
	out, err := m.runner.Run(ctx, deepRowCountCmd(binDir, scratch))
	if err != nil {
		return core.Result{}, m.recordDeepFail(ctx, started, sourceLabel, "restored cluster booted but the verification query failed: "+err.Error(), err)
	}
	rows, parsed := parseRowCount(out.Stdout)

	elapsed := time.Since(started)
	detail := "deep restore test passed: restored, booted (full WAL replay), and queried a throwaway scratch cluster"
	if !parsed {
		detail += "; row count unparseable, recorded as 0"
	}
	id := m.recordRestoreTestDetached(ctx, store.RestoreTestRecord{
		TestedAt:     started,
		SourceLabel:  sourceLabel,
		VerifiedRows: rows,
		Result:       "success",
		DurationMS:   elapsed.Milliseconds(),
		Detail:       detail,
	})

	result := core.Ok("deep restore test passed").
		WithData("stanza", stanza).
		WithData("method", "scratch restore + boot").
		WithData("verified_rows", rows).
		WithData("duration_ms", elapsed.Milliseconds())
	if sourceLabel != "" {
		result = result.WithData("source_label", sourceLabel)
	}
	if id > 0 {
		result = result.WithData("history_id", id)
	}
	return result, nil
}

// recordDeepFail records a failed deep restore-test row on a detached context and
// returns the underlying error, so a shutdown that cancels the operation ctx can
// never drop the record of a failed (and thus alarming) recovery drill.
func (m *Manager) recordDeepFail(ctx context.Context, started time.Time, sourceLabel, detail string, cause error) error {
	m.recordRestoreTestDetached(ctx, store.RestoreTestRecord{
		TestedAt:    started,
		SourceLabel: sourceLabel,
		Result:      "fail",
		DurationMS:  time.Since(started).Milliseconds(),
		Detail:      detail,
	})
	return cause
}

// recordRestoreTestDetached records a restore-test row on a context that
// survives cancellation of the operation ctx (e.g. shutdown), bounded by a short
// timeout, so a pass/fail record is never silently lost.
func (m *Manager) recordRestoreTestDetached(ctx context.Context, rec store.RestoreTestRecord) int64 {
	recCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	return m.recordRestoreTest(recCtx, rec)
}

// cleanupScratch always stops the scratch cluster (best-effort) and removes the
// scratch dir, on a detached context so shutdown cannot leave a half-booted
// cluster or fill the disk with abandoned scratch data. binDir is read through a
// pointer because it is only known after the restore resolves the version.
func (m *Manager) cleanupScratch(ctx context.Context, binDir *string, scratch string) {
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 90*time.Second)
	defer cancel()
	if binDir != nil && *binDir != "" {
		if _, err := m.runner.Run(stopCtx, pgCtlStopCmd(*binDir, scratch)); err != nil {
			m.log.Warn("restore-test scratch cluster stop failed (continuing with cleanup)", "dir", scratch, "error", err)
		}
	}
	if err := os.RemoveAll(scratch); err != nil {
		m.log.Error("failed to remove restore-test scratch dir", "dir", scratch, "error", err)
	}
}

// scratchPostgresqlConf is the minimal postgresql.conf written into the scratch
// data dir so pg_ctl can boot a Debian/PGDG-layout cluster restored from a
// PGDATA-only backup. Every runtime knob (port, listen_addresses,
// unix_socket_directories, archive_mode) is injected via pg_ctl -o, and recovery
// settings live in pgBackRest's restored postgresql.auto.conf, so this file needs
// no directives at all — it only has to EXIST, because on Debian the real
// postgresql.conf lives under /etc/postgresql/<major>/<cluster>/ (outside PGDATA)
// and is therefore never captured by a PGDATA-only restore.
const scratchPostgresqlConf = "# indiepg deep restore-test scratch cluster — minimal config.\n" +
	"# Runtime overrides (port, listen_addresses, unix_socket_directories,\n" +
	"# archive_mode) are injected via pg_ctl -o; recovery settings live in the\n" +
	"# restored postgresql.auto.conf. This file exists only so pg_ctl can boot a\n" +
	"# Debian-layout cluster whose real postgresql.conf lives under /etc and is\n" +
	"# thus absent from the restored PGDATA.\n"

// writeScratchClusterConfig materializes the configuration a Debian/PGDG cluster
// needs to boot from a restored PGDATA. On Debian, postgresql.conf, pg_hba.conf
// and pg_ident.conf live under /etc/postgresql/<major>/<cluster>/ — NOT inside
// PGDATA — so pgBackRest (which restores only PGDATA) never captures them and the
// scratch dir has none; pg_ctl then dies instantly with "could not access the
// server configuration file". It writes a minimal postgresql.conf, a
// `local all all trust` pg_hba.conf (so the private-socket row-count query can
// authenticate without a password), and an empty pg_ident.conf. config_file is
// deliberately NOT pointed at the live /etc path — the scratch cluster is fully
// self-contained. Each file is written only if absent, so a self-contained PGDATA
// (config already inside the data dir) is never clobbered, and is chowned to
// postgres (best-effort, root-only) to match the restored data dir.
func (m *Manager) writeScratchClusterConfig(scratch string) error {
	files := []struct {
		name string
		body string
	}{
		{"postgresql.conf", scratchPostgresqlConf},
		{"pg_hba.conf", "local all all trust\n"},
		{"pg_ident.conf", "# indiepg deep restore-test scratch cluster — no ident maps.\n"},
	}
	uid, gid, idErr := pgUserIDs()
	for _, f := range files {
		path := filepath.Join(scratch, f.name)
		if _, err := os.Stat(path); err == nil {
			continue // already present (self-contained PGDATA): do not clobber.
		} else if !os.IsNotExist(err) {
			return core.InternalError("stat scratch cluster config %q", path).Wrap(err)
		}
		if err := os.WriteFile(path, []byte(f.body), 0o600); err != nil {
			return core.InternalError("write scratch cluster config %q", path).Wrap(err)
		}
		// Match the restored data dir's ownership so postgres can read the config.
		// Best-effort: only matters (and only succeeds) when the panel runs as root.
		if os.Geteuid() == 0 && idErr == nil {
			if err := os.Chown(path, uid, gid); err != nil {
				m.log.Warn("could not chown scratch cluster config to postgres", "path", path, "error", err)
			}
		}
	}
	return nil
}

// chownScratch best-effort hands the scratch dir to the postgres user so
// pgBackRest (which runs as that user) can write into it. It only matters — and
// only succeeds — when the panel runs as root; otherwise it is a harmless no-op.
func (m *Manager) chownScratch(scratch string) {
	if os.Geteuid() != 0 {
		return
	}
	uid, gid, err := pgUserIDs()
	if err != nil {
		m.log.Warn("could not resolve postgres user to own restore-test scratch dir", "error", err)
		return
	}
	if err := os.Chown(scratch, uid, gid); err != nil {
		m.log.Warn("could not chown restore-test scratch dir to postgres", "dir", scratch, "error", err)
	}
}

// parseRowCount parses psql -At numeric output into a row count. It returns
// (0, false) when the output is absent or non-numeric so the caller can record
// the boot success honestly even if the count is unreadable.
func parseRowCount(stdout string) (int64, bool) {
	s := strings.TrimSpace(stdout)
	if s == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}
