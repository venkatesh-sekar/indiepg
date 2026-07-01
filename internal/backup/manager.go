package backup

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/venkatesh-sekar/indiepg/internal/config"
	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/exec"
	"github.com/venkatesh-sekar/indiepg/internal/identity"
	"github.com/venkatesh-sekar/indiepg/internal/store"
)

// Manager orchestrates pgBackRest operations with single-writer ownership
// enforcement and store-backed history.
//
// Before any repo-writing operation (backup), the Manager claims (or verifies)
// ownership of the configured repo prefix and updates its heartbeat. A foreign,
// non-stale owner is a HARD STOP: the operation aborts with a
// *core.OwnershipError before pgBackRest is ever invoked, because two panels
// sharing a repository would corrupt both.
type Manager struct {
	runner exec.Runner
	store  *store.Store
	log    *core.Logger
	// confDir is the directory holding the managed pgbackrest.conf. It defaults
	// to /etc/pgbackrest and is overridable (tests) so the config-writing path is
	// exercisable without touching the real system directory.
	confDir string

	// mu guards cfg and owner, which Reconfigure swaps when the operator saves new
	// settings (e.g. wiring an S3 target + ownership guard) without restarting the
	// panel. Every read goes through config()/currentOwner().
	mu    sync.RWMutex
	cfg   config.Config
	owner *identity.Owner

	// backupMu is a process-local single-flight guard for repo-writing backups.
	// pgBackRest already takes its own on-disk lock, but a second concurrent
	// attempt would lose that lock and be recorded as a FAILED backup — tripping
	// the critical backup-failed alert on what is really just an overlap (a manual
	// backup during a scheduled one, or a long full overlapping the next
	// incremental). Held via TryLock so an overlap becomes a clean, typed skip
	// (CodeConflict) with no failure row, never a false alarm.
	backupMu sync.Mutex

	// outcomeMu guards lastOutcome, the most recent backup result observed this
	// process. The immediate backup-failed alert is normally derived from the
	// newest backup_history row, but recordBackup is best-effort — if a failed
	// backup's history insert ALSO fails (store contention / disk pressure on the
	// panel volume), the newest row stays the prior success and the alert goes
	// silent. lastOutcome records the result independent of that store write so
	// the telemetry collector can keep the alert loud. See LastOutcome.
	outcomeMu   sync.RWMutex
	lastOutcome backupOutcome

	// Deep restore-test seams. scratchRoot is the directory the deep restore-test
	// restores its throwaway scratch cluster into (never the live data dir).
	// diskFree and resolvePGBin are injectable so the orchestration is unit-testable
	// without a real filesystem volume or a Postgres install (see restore_deep.go).
	scratchRoot  string
	diskFree     func(path string) (uint64, error)
	resolvePGBin func(dataDir string) (string, error)

	// cluster stops/starts the live managed cluster around a destructive restore.
	// pgBackRest refuses to restore over a running cluster (ERROR [038]), so
	// Restore stops it first and starts it after (PostgreSQL then replays WAL to
	// the recovery target and promotes). It is nil only for local-only uses (and
	// in unit tests that exercise Restore without a cluster), where Restore keeps
	// its legacy behaviour of running the restore directly.
	cluster ClusterController
}

// ClusterController stops and starts the live managed Postgres cluster around a
// destructive restore. It is satisfied structurally by *pg.Manager
// (StopCluster/StartCluster), wired in by the server so the backup package does
// not import internal/pg.
type ClusterController interface {
	StopCluster(ctx context.Context) error
	StartCluster(ctx context.Context) error
}

// config returns the current configuration snapshot under the read lock.
func (m *Manager) config() config.Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg
}

// currentOwner returns the current ownership guard (possibly nil) under the read
// lock.
func (m *Manager) currentOwner() *identity.Owner {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.owner
}

// Reconfigure swaps the live configuration and ownership guard. The server calls
// it after a settings save so a freshly-configured S3 target (and its
// single-writer Owner) takes effect immediately, without a restart. A nil owner
// is valid (local-only, or S3 with the guard unavailable — see acquireForWrite).
func (m *Manager) Reconfigure(cfg config.Config, owner *identity.Owner) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cfg = cfg
	m.owner = owner
}

// Options configure a Manager. Owner may be nil only for an explicitly
// local-only repo (no remote/S3 target configured), where there is no shared
// resource to corrupt. When a remote/S3 target IS configured, a nil Owner is
// fail-closed: repo writes return an error rather than silently dropping the
// single-writer guard.
type Options struct {
	Runner exec.Runner
	Store  *store.Store
	Config config.Config
	Owner  *identity.Owner // claims/heartbeats the repo before writing
	Logger *core.Logger
	// ConfDir overrides where the managed pgbackrest.conf is written. Empty uses
	// the default /etc/pgbackrest. Intended for tests.
	ConfDir string
	// ScratchRoot is the directory under which the deep restore-test creates its
	// throwaway scratch cluster. Empty uses the OS temp dir. Point it at a volume
	// with headroom; the deep test never writes anywhere else and always cleans up.
	ScratchRoot string
	// Cluster stops/starts the live managed cluster around a destructive restore.
	// Required for a real restore (pgBackRest refuses to restore over a running
	// cluster); nil is allowed for local-only/test use, where Restore runs the
	// pgBackRest restore directly without a stop/start.
	Cluster ClusterController
}

// New builds a Manager from Options. A nil logger is replaced with a discard
// logger.
func New(opts Options) *Manager {
	log := opts.Logger
	if log == nil {
		log = core.Discard()
	}
	confDir := opts.ConfDir
	if confDir == "" {
		confDir = defaultConfDir
	}
	scratchRoot := opts.ScratchRoot
	if scratchRoot == "" {
		scratchRoot = os.TempDir()
	}
	return &Manager{
		runner:       opts.Runner,
		store:        opts.Store,
		cfg:          opts.Config,
		owner:        opts.Owner,
		log:          log,
		confDir:      confDir,
		scratchRoot:  scratchRoot,
		diskFree:     defaultDiskFree,
		resolvePGBin: defaultResolvePGBin,
		cluster:      opts.Cluster,
	}
}

// repoPrefix is the ownership-marker prefix for this manager's repo. It is the
// configured backup prefix (namespaced by instance id at install time).
func (m *Manager) repoPrefix() string {
	return m.config().Backup.Prefix
}

// Info parses the current backups from the stanza by running `pgbackrest info`
// and parsing its JSON. It is a read-only operation and does not touch
// ownership.
func (m *Manager) Info(ctx context.Context) ([]BackupInfo, error) {
	stanza := m.config().Stanza
	if err := validateStanza(stanza); err != nil {
		return nil, err
	}
	res, err := m.runner.Run(ctx, InfoCmd(stanza))
	if err != nil {
		return nil, err
	}
	return ParseInfoJSON([]byte(res.Stdout), stanza)
}

// Backup claims/verifies ownership of the repo, heartbeats, runs pgBackRest of
// the requested type, and records the result in store.backup_history.
//
// A foreign owner is a HARD STOP: it returns *core.OwnershipError and never
// runs pgBackRest. A pgBackRest failure is recorded as a failed history row and
// returned as the underlying exec error.
func (m *Manager) Backup(ctx context.Context, t Type) (core.Result, error) {
	stanza := m.config().Stanza
	if err := validateStanza(stanza); err != nil {
		return core.Result{}, err
	}
	if _, err := ParseType(string(t)); err != nil {
		return core.Result{}, err
	}

	// Process-local single-flight: an overlapping backup is a benign SKIP, not a
	// failure. Without this, a manual backup launched during a scheduled one (or a
	// long full overlapping the next incremental) would collide on pgBackRest's own
	// lock, be recorded as a "fail" row, and raise a false backup-failed alert.
	if !m.backupMu.TryLock() {
		return core.Result{}, core.ConflictError("a backup is already running; skipping this %s backup", t).
			WithHint("backups run one at a time; the in-progress backup will finish on its own")
	}
	defer m.backupMu.Unlock()

	// Single-writer ownership: claim (or verify mine), then heartbeat. A foreign
	// non-stale owner returns *core.OwnershipError and stops us cold.
	if err := m.acquireForWrite(ctx); err != nil {
		return core.Result{}, err
	}

	started := time.Now().UTC()
	rec, runErr := m.executeBackup(ctx, stanza, t, started)

	// Persist the terminal row regardless of outcome, on a detached context: a
	// shutdown that cancels the operation ctx must not also drop the record of how
	// the backup ended.
	recCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	id := m.recordBackup(recCtx, rec)
	if runErr != nil {
		return core.Result{}, runErr
	}
	return backupResult(string(t), stanza, rec, id), nil
}

// StartBackup begins an asynchronous backup and returns the id of its history
// row immediately, so the HTTP handler can respond at once and the UI can poll
// backup history for live status instead of holding a request open for the whole
// — potentially hours-long — run.
//
// The fast, fail-fast gates run synchronously so the caller still gets a clean
// inline error rather than a phantom row, exactly matching Backup: an overlapping
// backup is a typed CodeConflict (no row), and a foreign-owned repo is a HARD
// STOP *core.OwnershipError (no row). Only once those pass is a "running" row
// inserted and the pgBackRest run handed to a background goroutine, which updates
// that row to its terminal state (success/fail) when it finishes.
//
// The single-flight guard is acquired here and released by the goroutine, so it
// spans the entire run exactly as the synchronous Backup's defer does.
func (m *Manager) StartBackup(ctx context.Context, t Type) (int64, error) {
	stanza := m.config().Stanza
	if err := validateStanza(stanza); err != nil {
		return 0, err
	}
	if _, err := ParseType(string(t)); err != nil {
		return 0, err
	}
	if m.store == nil {
		return 0, core.InternalError("cannot start an asynchronous backup without a store")
	}

	// Process-local single-flight: an overlapping backup is a benign SKIP, not a
	// failure (see Backup). No row is written, so an overlap never raises a false
	// backup-failed alert.
	if !m.backupMu.TryLock() {
		return 0, core.ConflictError("a backup is already running; skipping this %s backup", t).
			WithHint("backups run one at a time; the in-progress backup will finish on its own")
	}

	// The lock is now held. Every early-return path below must release it; on the
	// success path ownership transfers to the background goroutine instead.
	release := true
	defer func() {
		if release {
			m.backupMu.Unlock()
		}
	}()

	// Claim ownership synchronously so a foreign-owner HARD STOP surfaces inline
	// (no "running" row), mirroring Backup.
	if err := m.acquireForWrite(ctx); err != nil {
		return 0, err
	}

	started := time.Now().UTC()
	insCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	id, err := m.store.InsertBackup(insCtx, store.BackupRecord{
		BackupType: string(t),
		StartedAt:  started,
		Result:     "running",
		RepoPath:   m.repoPrefix(),
	})
	if err != nil {
		return 0, err
	}

	// Hand the long run to a goroutine on a background context (NOT the request
	// ctx, which is cancelled when the handler returns — see the migration worker
	// for the same discipline). The goroutine owns the single-flight lock until
	// the run completes, then updates the row in place to its terminal state.
	release = false
	go func() {
		defer m.backupMu.Unlock()
		defer func() {
			if r := recover(); r != nil {
				m.log.Error("panic in async backup goroutine", "id", id, "panic", r)
				m.recordOutcome(time.Now().UTC(), true)
				upCtx, upCancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer upCancel()
				rec := store.BackupRecord{
					ID:         id,
					BackupType: string(t),
					StartedAt:  started,
					Result:     "fail",
					Error:      fmt.Sprintf("internal panic: %v", r),
					RepoPath:   m.repoPrefix(),
				}
				stopped := time.Now().UTC()
				rec.StoppedAt = &stopped
				if uerr := m.updateBackup(upCtx, rec); uerr != nil {
					m.log.Error("failed to update backup history after panic", "id", id, "error", uerr)
				}
			}
		}()
		rec, _ := m.executeBackup(context.Background(), stanza, t, started)
		rec.ID = id
		upCtx, upCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer upCancel()
		if uerr := m.updateBackup(upCtx, rec); uerr != nil {
			m.log.Error("failed to update backup history after async run", "id", id, "error", uerr)
		}
	}()
	return id, nil
}

// executeBackup runs pgBackRest of the requested type and builds the terminal
// history record (without persisting it), recording the in-memory outcome first
// so the backup-failed alert stays honest even if the row write later fails. The
// caller must already hold the single-flight guard and have claimed ownership.
// On success the record is enriched, best-effort, with stats from `pgbackrest
// info`. It is shared by the synchronous Backup (which inserts the record) and
// the asynchronous StartBackup (which updates a pre-inserted "running" row).
func (m *Manager) executeBackup(ctx context.Context, stanza string, t Type, started time.Time) (store.BackupRecord, error) {
	_, runErr := m.runner.Run(ctx, BackupCmd(stanza, t))
	stopped := time.Now().UTC()

	// Remember this outcome in memory FIRST, independent of the history-row write
	// (see lastOutcome): if the row write fails, the in-memory signal keeps the
	// backup-failed alert honest.
	m.recordOutcome(stopped, runErr != nil)

	rec := store.BackupRecord{
		BackupType: string(t),
		StartedAt:  started,
		StoppedAt:  &stopped,
		RepoPath:   m.repoPrefix(),
	}
	if runErr != nil {
		rec.Result = "fail"
		rec.Error = runErr.Error()
		return rec, runErr
	}
	rec.Result = "success"

	// Best-effort: enrich the history row with stats from the just-created
	// backup (newest first). A parse/info failure here must not fail the backup.
	if info, err := m.Info(ctx); err == nil && len(info) > 0 {
		latest := info[0]
		rec.Label = latest.Label
		rec.SizeBytes = latest.Size
		rec.DatabaseBytes = latest.DatabaseSize
		rec.RepoBytes = latest.RepoSize
		rec.WALStart = latest.WALStart
		rec.WALStop = latest.WALStop
		if !latest.StartTime.IsZero() {
			rec.StartedAt = latest.StartTime
		}
		if !latest.StopTime.IsZero() {
			stop := latest.StopTime
			rec.StoppedAt = &stop
		}
	} else if err != nil {
		m.log.Warn("backup succeeded but info refresh failed", "error", err)
	}
	return rec, nil
}

// backupResult builds the success Result returned by a completed backup from its
// terminal record. id <= 0 (a failed history write) simply omits history_id.
func backupResult(t, stanza string, rec store.BackupRecord, id int64) core.Result {
	result := core.Ok("backup completed").
		WithData("type", t).
		WithData("stanza", stanza).
		WithData("repo_size_bytes", rec.RepoBytes).
		WithData("database_size_bytes", rec.DatabaseBytes)
	if rec.Label != "" {
		result = result.WithData("label", rec.Label)
	}
	if id > 0 {
		result = result.WithData("history_id", id)
	}
	return result
}

// Restore runs a guarded restore. A PITR/overwrite restore is destructive — it
// rewinds the live cluster — so it requires typed-name confirmation equal to the
// stanza name (confirmTyped == stanza). A latest-WAL restore with no target and
// no delta still overwrites the data directory, so confirmation is required in
// all cases.
//
// Because the restore irreversibly overwrites the live cluster, a safety backup
// of the current state is taken first (Safety DNA, design §5.5/§7). If that
// safety backup fails, the restore is a HARD STOP: it returns the failure with
// recovery instructions and never invokes pgBackRest restore. On success the
// safety backup label is recorded on the Result ("safety_backup_label") so the
// operator has an explicit recovery point.
//
// Restore does not write the repo (other than the safety backup), so it verifies
// (not claims) ownership when an Owner is configured: restoring from a repo owned
// by a foreign panel is a HARD STOP.
func (m *Manager) Restore(ctx context.Context, target *RecoveryTarget, delta bool, confirmTyped string) (core.Result, error) {
	stanza := m.config().Stanza
	if err := validateStanza(stanza); err != nil {
		return core.Result{}, err
	}
	if target != nil {
		if err := target.Validate(); err != nil {
			return core.Result{}, err
		}
	}

	// Typed-name confirmation gates the destructive overwrite.
	if serr := core.RequireConfirmation("restore stanza "+stanza, stanza, confirmTyped); serr != nil {
		return core.Result{}, serr
	}

	// Verify we own (or the repo is unclaimed by) the repo we restore from.
	if err := m.verifyForRead(ctx); err != nil {
		return core.Result{}, err
	}

	// Safety net: snapshot the current cluster before the irreversible overwrite.
	// A failed safety backup is a HARD STOP — we must never overwrite the live
	// cluster without a recovery point. Backup also claims/heartbeats ownership,
	// which is appropriate since the safety backup writes the repo.
	safetyLabel, serr := m.takeSafetyBackup(ctx)
	if serr != nil {
		return core.Result{}, serr
	}

	// Pin the backup set so the recovery target stays reachable even though the
	// safety backup we just took is now the NEWEST set (BUG-3). pgBackRest only
	// auto-selects an older set for --type=time; for xid/lsn/name it picks the
	// newest (the safety backup), which already contains state PAST the target. So
	// for any non-zero target, select an appropriate older set and restore --set
	// from it. A zero target (recover-to-latest) is left untouched: pgBackRest
	// auto-selects as before. We work on a copy so the caller's target is unchanged.
	restoreTarget := target
	if target != nil && !target.IsZero() {
		sel := *target
		if label := m.selectRestoreSet(ctx, &sel, safetyLabel); label != "" {
			sel.Set = label
		}
		restoreTarget = &sel
	}

	spec, err := RestoreCmd(stanza, restoreTarget, delta)
	if err != nil {
		return core.Result{}, err
	}

	// pgBackRest refuses to restore over a RUNNING cluster (ERROR [038]: unable to
	// restore while PostgreSQL is running, because postmaster.pid is present). So
	// stop the managed cluster, run the restore into the now-stopped data dir, then
	// start it again — on start PostgreSQL replays WAL to the recovery target and
	// promotes. The safety backup above was taken while the cluster was still up.
	//
	// Safety ordering:
	//   - A failed STOP is a HARD STOP before the restore: the data dir is untouched
	//     and the cluster is still up, so we surface the error and restore nothing.
	//   - After the restore (success OR failure) we ALWAYS attempt a START, on a
	//     context detached from cancellation, so a cancelled request or a failed
	//     restore can never leave the box with PostgreSQL down.
	if m.cluster != nil {
		if serr := m.cluster.StopCluster(ctx); serr != nil {
			return core.Result{}, core.ExecError(
				"refusing to restore: could not stop PostgreSQL before the restore; the live cluster was left untouched and is still running").Wrap(serr)
		}
	}

	_, runErr := m.runner.Run(ctx, spec)

	if m.cluster != nil {
		// Detached so a cancelled operation ctx never leaves PostgreSQL down.
		startCtx := context.WithoutCancel(ctx)
		if startErr := m.cluster.StartCluster(startCtx); startErr != nil {
			if runErr != nil {
				return core.Result{}, core.InternalError(
					"restore failed AND restarting PostgreSQL afterwards also failed — manual recovery is required").
					WithDetail("restore_error", runErr.Error()).
					WithDetail("start_error", startErr.Error())
			}
			return core.Result{}, core.ExecError(
				"restore completed but restarting PostgreSQL (to replay WAL and promote) failed; start the cluster manually").Wrap(startErr)
		}
	}
	if runErr != nil {
		return core.Result{}, runErr
	}

	result := core.Ok("restore completed").
		WithData("stanza", stanza).
		WithData("delta", delta)
	if safetyLabel != "" {
		result = result.WithData("safety_backup_label", safetyLabel)
	}
	if target != nil && !target.IsZero() {
		result = result.WithData("pitr", true)
	}
	return result, nil
}

// selectRestoreSet enumerates the available backup sets and returns the label
// the restore should pin via --set so target is reachable (or "" to let
// pgBackRest auto-select). safetyLabel is the just-taken pre-restore safety
// backup, which is always the NEWEST set and must never be the base for a target
// that precedes it (BUG-3).
//
// Enumeration is best-effort: a failed `info` must not block the restore. Without
// an explicit set pgBackRest falls back to its auto-selection — that is the
// pre-existing behaviour, not a new regression — so we log and proceed.
func (m *Manager) selectRestoreSet(ctx context.Context, target *RecoveryTarget, safetyLabel string) string {
	backups, err := m.Info(ctx)
	if err != nil {
		m.log.Warn("could not enumerate backups to pin the restore set; pgBackRest will auto-select", "error", err)
		return ""
	}
	return chooseRestoreSet(target, safetyLabel, backups)
}

// chooseRestoreSet selects the backup-set label to restore from for target.
// backups must be newest-first (as Info returns). It returns "" for a zero target
// or when no suitable set exists, leaving pgBackRest to auto-select.
func chooseRestoreSet(target *RecoveryTarget, safetyLabel string, backups []BackupInfo) string {
	if target == nil || target.IsZero() {
		return ""
	}
	// TIME target: recovery stops at a known wall-clock, so pick the newest set
	// whose stop precedes it — exactly the set selection pgBackRest already does
	// for --type=time, made explicit so it is consistent with the types below.
	if target.Time != nil {
		want := *target.Time
		for _, b := range backups { // newest-first
			if !b.StopTime.IsZero() && !b.StopTime.After(want) {
				return b.Label
			}
		}
		return ""
	}
	// xid/lsn/name target: there is no wall-clock for the target, so we cannot
	// compare it to backup stop times directly. But the target was generated BEFORE
	// this restore, and the pre-restore safety backup (taken moments ago) is the
	// NEWEST set and already contains state PAST the target — so it can never be the
	// base. Pin the newest set OLDER than the safety backup; its WAL is replayed
	// forward to the target.
	safetyStop := stopTimeOf(safetyLabel, backups)
	for _, b := range backups { // newest-first
		if b.Label == safetyLabel {
			continue
		}
		// Defensive: never pick a set at/after the safety backup (it would already be
		// past the target). The safety backup is normally strictly newest, so this
		// only guards odd ordering.
		if !safetyStop.IsZero() && !b.StopTime.IsZero() && !b.StopTime.Before(safetyStop) {
			continue
		}
		return b.Label
	}
	return ""
}

// stopTimeOf returns the stop time of the backup with the given label, or the
// zero time if no such backup is present.
func stopTimeOf(label string, backups []BackupInfo) time.Time {
	for _, b := range backups {
		if b.Label == label {
			return b.StopTime
		}
	}
	return time.Time{}
}

// RestoreTest verifies that the backup repository is intact and recoverable
// WITHOUT performing a restore. It runs `pgbackrest verify`, which checks that
// every backup and WAL file in the repo is present and matches its recorded
// checksum/size — the cheapest meaningful proof that a restore could succeed.
//
// This is the deliberate first slice of restore-verification: it never touches
// the live data directory, needs no scratch space or disk-headroom precheck, and
// cannot itself cause data loss. A deeper scratch-restore-and-boot proof (which
// would populate VerifiedRows with a real row count) is a separate, heavier
// capability and intentionally not done here.
//
// Like Restore, RestoreTest only reads the repo, so it verifies (does not claim)
// ownership: a foreign, non-stale owner is a HARD STOP. The outcome — pass or
// fail — is recorded in store.restore_tests regardless, so the durability
// surfacing can answer "have my backups been proven recoverable, and when?".
func (m *Manager) RestoreTest(ctx context.Context) (core.Result, error) {
	stanza := m.config().Stanza
	if err := validateStanza(stanza); err != nil {
		return core.Result{}, err
	}

	// Reading a foreign-owned repo is a HARD STOP (mirrors Restore's read side).
	if err := m.verifyForRead(ctx); err != nil {
		return core.Result{}, err
	}

	// Label the backup being verified (newest, best-effort) so the history row
	// records WHAT was checked. An info failure here must not fail the verify.
	sourceLabel := ""
	if info, err := m.Info(ctx); err == nil && len(info) > 0 {
		sourceLabel = info[0].Label
	}

	started := time.Now().UTC()
	_, runErr := m.runner.Run(ctx, VerifyCmd(stanza))
	elapsed := time.Since(started)

	rec := store.RestoreTestRecord{
		TestedAt:    started,
		SourceLabel: sourceLabel,
		DurationMS:  elapsed.Milliseconds(),
	}
	if runErr != nil {
		rec.Result = "fail"
		rec.Detail = "pgbackrest verify failed: " + runErr.Error()
		// Record on a detached context so a shutdown that cancels the operation
		// ctx never drops the record of a failed (and thus alarming) verification.
		recCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		m.recordRestoreTest(recCtx, rec)
		return core.Result{}, runErr
	}
	rec.Result = "success"
	rec.Detail = "pgbackrest verify passed: repository integrity confirmed (no restore performed)"

	recCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	id := m.recordRestoreTest(recCtx, rec)

	result := core.Ok("restore test passed").
		WithData("stanza", stanza).
		WithData("method", "pgbackrest verify").
		WithData("duration_ms", elapsed.Milliseconds())
	if sourceLabel != "" {
		result = result.WithData("source_label", sourceLabel)
	}
	if id > 0 {
		result = result.WithData("history_id", id)
	}
	return result, nil
}

// remoteTargetConfigured reports whether a remote/S3 backup destination is
// wired (a bucket or endpoint is set). A repo with neither is treated as
// explicitly local-only, where there is no shared resource to corrupt and the
// single-writer guard may be safely skipped.
func (m *Manager) remoteTargetConfigured() bool {
	cfg := m.config()
	return cfg.Backup.Bucket != "" || cfg.Backup.Endpoint != ""
}

// acquireForWrite claims (or verifies mine) the repo and heartbeats. A nil Owner
// is fail-closed when a remote/S3 target is configured: rather than silently
// dropping the single-writer guard (two panels sharing a repo would corrupt
// both), it returns an ownership error. Only an explicitly local-only repo (no
// bucket/endpoint) is allowed to proceed without an Owner. A foreign owner is
// propagated unchanged (HARD STOP).
func (m *Manager) acquireForWrite(ctx context.Context) error {
	owner := m.currentOwner()
	if owner == nil {
		if m.remoteTargetConfigured() {
			oe := core.NewOwnershipError(
				m.repoPrefix(), "", "", "", false,
				"refusing to write remote backup repo %q without a single-writer ownership guard; wire an Owner so two panels can never share (and corrupt) the same repository",
				m.repoPrefix(),
			)
			oe.Err = oe.Err.WithHint("construct the backup Manager with Options.Owner set (identity.NewOwner)")
			return oe
		}
		m.log.Warn("no ownership guard configured; proceeding without single-writer protection (local-only repo)")
		return nil
	}
	prefix := m.repoPrefix()
	if _, err := owner.Claim(ctx, prefix); err != nil {
		return err
	}
	if err := owner.Heartbeat(ctx, prefix); err != nil {
		return err
	}
	return nil
}

// takeSafetyBackup snapshots the current cluster with a full backup before a
// destructive restore overwrites it. It returns the resulting backup label (for
// the operator's recovery point) or, on failure, a HARD-STOP *core.SafetyError
// carrying recovery instructions — the caller must not proceed with the restore.
//
// It reuses Backup, so the safety snapshot is recorded in backup_history and
// goes through the same single-writer ownership discipline.
func (m *Manager) takeSafetyBackup(ctx context.Context) (string, error) {
	res, err := m.Backup(ctx, TypeFull)
	if err != nil {
		m.log.Error("safety backup before restore failed; refusing to overwrite the live cluster", "error", err)
		return "", core.NewSafetyError(
			"restore stanza "+m.config().Stanza,
			[]string{"a successful safety backup of the current cluster"},
			"refusing to restore: the pre-restore safety backup failed (%v); the live cluster was left untouched — fix the backup target/repo and retry, or take a manual backup before restoring",
			err,
		)
	}
	label, _ := res.Data["label"].(string)
	return label, nil
}

// verifyForRead verifies the repo is mine (or unclaimed) for a read-side
// operation (restore). A nil Owner degrades to a warning. An unclaimed repo
// (*core.NotFoundError) is acceptable for a read — we are not corrupting it — so
// it is not treated as fatal.
func (m *Manager) verifyForRead(ctx context.Context) error {
	owner := m.currentOwner()
	if owner == nil {
		m.log.Warn("no ownership guard configured; restoring without single-writer verification")
		return nil
	}
	_, err := owner.Verify(ctx, m.repoPrefix())
	if err == nil {
		return nil
	}
	// An unclaimed repo is fine to read from; only a foreign owner is fatal.
	if core.CodeOf(err) == core.CodeNotFound {
		return nil
	}
	return err
}

// backupOutcome is the most recent backup result observed this process, used as
// a store-independent source for the immediate backup-failed alert.
type backupOutcome struct {
	at     time.Time
	failed bool
	valid  bool // false until the first backup of this process completes
}

// recordOutcome remembers the result of a just-finished backup run in memory,
// independent of whether its history row was persisted.
func (m *Manager) recordOutcome(at time.Time, failed bool) {
	m.outcomeMu.Lock()
	defer m.outcomeMu.Unlock()
	m.lastOutcome = backupOutcome{at: at, failed: failed, valid: true}
}

// LastOutcome reports the most recent backup result observed this process (its
// completion time and whether it failed). ok is false until the first backup of
// this process finishes. It reflects the actual run regardless of whether the
// backup_history row was successfully written, so the telemetry collector can
// raise the backup-failed alert even when the failure-row insert itself failed.
func (m *Manager) LastOutcome() (at time.Time, failed bool, ok bool) {
	m.outcomeMu.RLock()
	defer m.outcomeMu.RUnlock()
	return m.lastOutcome.at, m.lastOutcome.failed, m.lastOutcome.valid
}

// recordBackup inserts a history row, logging (not failing) on a store error so
// a transient store hiccup never masks a successful backup. Returns the new row
// id (0 on failure or when no store is configured).
func (m *Manager) recordBackup(ctx context.Context, rec store.BackupRecord) int64 {
	if m.store == nil {
		return 0
	}
	id, err := m.store.InsertBackup(ctx, rec)
	if err != nil {
		m.log.Error("failed to record backup history", "error", err)
		return 0
	}
	return id
}

// updateBackup overwrites an existing history row (by rec.ID) with a completed
// run's terminal state. Used by the asynchronous path to resolve the "running"
// row it inserted up front. A nil store is a no-op (mirrors recordBackup).
func (m *Manager) updateBackup(ctx context.Context, rec store.BackupRecord) error {
	if m.store == nil {
		return nil
	}
	return m.store.UpdateBackup(ctx, rec)
}

// recordRestoreTest inserts a restore-test history row, logging (not failing) on
// a store error so a transient store hiccup never masks the verification result.
// Returns the new row id (0 on failure or when no store is configured).
func (m *Manager) recordRestoreTest(ctx context.Context, rec store.RestoreTestRecord) int64 {
	if m.store == nil {
		return 0
	}
	id, err := m.store.InsertRestoreTest(ctx, rec)
	if err != nil {
		m.log.Error("failed to record restore-test history", "error", err)
		return 0
	}
	return id
}
