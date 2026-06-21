package backup

import (
	"context"
	"time"

	"github.com/venkatesh-sekar/pgpanel/internal/config"
	"github.com/venkatesh-sekar/pgpanel/internal/core"
	"github.com/venkatesh-sekar/pgpanel/internal/exec"
	"github.com/venkatesh-sekar/pgpanel/internal/identity"
	"github.com/venkatesh-sekar/pgpanel/internal/store"
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
	cfg    config.Config
	owner  *identity.Owner
	log    *core.Logger
}

// Options configure a Manager. Owner may be nil only in degraded contexts where
// ownership cannot be enforced (e.g. no S3 target configured); when nil, repo
// writes proceed without the single-writer guard and a warning is logged.
type Options struct {
	Runner exec.Runner
	Store  *store.Store
	Config config.Config
	Owner  *identity.Owner // claims/heartbeats the repo before writing
	Logger *core.Logger
}

// New builds a Manager from Options. A nil logger is replaced with a discard
// logger.
func New(opts Options) *Manager {
	log := opts.Logger
	if log == nil {
		log = core.Discard()
	}
	return &Manager{
		runner: opts.Runner,
		store:  opts.Store,
		cfg:    opts.Config,
		owner:  opts.Owner,
		log:    log,
	}
}

// repoPrefix is the ownership-marker prefix for this manager's repo. It is the
// configured backup prefix (namespaced by instance id at install time).
func (m *Manager) repoPrefix() string {
	return m.cfg.Backup.Prefix
}

// Info parses the current backups from the stanza by running `pgbackrest info`
// and parsing its JSON. It is a read-only operation and does not touch
// ownership.
func (m *Manager) Info(ctx context.Context) ([]BackupInfo, error) {
	stanza := m.cfg.Stanza
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
	stanza := m.cfg.Stanza
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
		m.recordBackup(ctx, rec)
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

	id := m.recordBackup(ctx, rec)

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
// Restore does not write the repo, so it verifies (not claims) ownership when an
// Owner is configured: restoring from a repo owned by a foreign panel is a HARD
// STOP.
func (m *Manager) Restore(ctx context.Context, target *RecoveryTarget, delta bool, confirmTyped string) (core.Result, error) {
	stanza := m.cfg.Stanza
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
	if target != nil && !target.IsZero() {
		result = result.WithData("pitr", true)
	}
	return result, nil
}

// acquireForWrite claims (or verifies mine) the repo and heartbeats. A nil Owner
// degrades to a logged warning so the manager remains usable in test/no-S3
// contexts; a foreign owner is propagated unchanged (HARD STOP).
func (m *Manager) acquireForWrite(ctx context.Context) error {
	if m.owner == nil {
		m.log.Warn("no ownership guard configured; proceeding without single-writer protection")
		return nil
	}
	prefix := m.repoPrefix()
	if _, err := m.owner.Claim(ctx, prefix); err != nil {
		return err
	}
	if err := m.owner.Heartbeat(ctx, prefix); err != nil {
		return err
	}
	return nil
}

// verifyForRead verifies the repo is mine (or unclaimed) for a read-side
// operation (restore). A nil Owner degrades to a warning. An unclaimed repo
// (*core.NotFoundError) is acceptable for a read — we are not corrupting it — so
// it is not treated as fatal.
func (m *Manager) verifyForRead(ctx context.Context) error {
	if m.owner == nil {
		m.log.Warn("no ownership guard configured; restoring without single-writer verification")
		return nil
	}
	_, err := m.owner.Verify(ctx, m.repoPrefix())
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
