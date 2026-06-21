package backup

import (
	"context"
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
	return &Manager{
		runner:  opts.Runner,
		store:   opts.Store,
		cfg:     opts.Config,
		owner:   opts.Owner,
		log:     log,
		confDir: confDir,
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

	// Single-writer ownership: claim (or verify mine), then heartbeat. A foreign
	// non-stale owner returns *core.OwnershipError and stops us cold.
	if err := m.acquireForWrite(ctx); err != nil {
		return core.Result{}, err
	}

	started := time.Now().UTC()
	_, runErr := m.runner.Run(ctx, BackupCmd(stanza, t))
	stopped := time.Now().UTC()

	// Record the run regardless of outcome.
	rec := store.BackupRecord{
		BackupType: string(t),
		StartedAt:  started,
		StoppedAt:  &stopped,
		RepoPath:   m.repoPrefix(),
	}
	if runErr != nil {
		rec.Result = "fail"
		rec.Error = runErr.Error()
		// Persist the terminal failure row on a detached context: a shutdown that
		// cancels the operation ctx must not also drop the record of why the
		// backup failed.
		recCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		m.recordBackup(recCtx, rec)
		return core.Result{}, runErr
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

	// Persist the terminal success row on a detached context too, so a ctx
	// cancelled between the run finishing and the insert never loses the row.
	recCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	id := m.recordBackup(recCtx, rec)

	result := core.Ok("backup completed").
		WithData("type", string(t)).
		WithData("stanza", stanza).
		WithData("repo_size_bytes", rec.RepoBytes).
		WithData("database_size_bytes", rec.DatabaseBytes)
	if rec.Label != "" {
		result = result.WithData("label", rec.Label)
	}
	if id > 0 {
		result = result.WithData("history_id", id)
	}
	return result, nil
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

	spec, err := RestoreCmd(stanza, target, delta)
	if err != nil {
		return core.Result{}, err
	}
	if _, err := m.runner.Run(ctx, spec); err != nil {
		return core.Result{}, err
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
