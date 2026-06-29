package server

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/venkatesh-sekar/indiepg/internal/backup"
	"github.com/venkatesh-sekar/indiepg/internal/config"
	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/pg"
)

// This file implements the PostgreSQL version-selection & upgrade API (design
// §7). Read endpoints (version, status) are synchronous; the long-running
// mutating operations (minor upgrade, major start, finalize, rollback) follow
// the same async pattern as backup/migration: the handler runs the fast gates
// synchronously, then hands the run to a background goroutine and returns
// immediately with the operation status the SPA polls via GET
// /api/pg/upgrade/status. The major preflight is synchronous because its §7
// contract is an inline {checks, preview} response, not an operation handle.
//
// A single process-wide lock (s.upgradeMu) plus the durable pending-finalization
// state make every upgrade mutually exclusive: a second upgrade is refused while
// one is in progress OR while the box awaits finalization (§10).

// --- GET /api/pg/version ---

type pgCurrentVersion struct {
	Full  string `json:"full"`
	Major int    `json:"major"`
}

type pgMinorUpdate struct {
	Available bool   `json:"available"`
	Target    string `json:"target"`
}

type pgMajorOption struct {
	Major   int  `json:"major"`
	Default bool `json:"default,omitempty"`
}

type pgAvailableUpdates struct {
	Minor  pgMinorUpdate   `json:"minor"`
	Majors []pgMajorOption `json:"majors"`
}

type pgVersionResponse struct {
	Running             bool                    `json:"running"`
	Current             pgCurrentVersion        `json:"current"`
	Available           pgAvailableUpdates      `json:"available"`
	PendingFinalization *pg.PendingFinalization `json:"pending_finalization"`
}

// handleGetPGVersion drives the Version panel + dashboard line: the running
// version, available minor/major updates, and any pending finalization. Read-
// only; not audited.
func (s *Server) handleGetPGVersion(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	full, major, running, err := s.pg.CurrentVersion(ctx)
	if err != nil {
		writeError(w, err)
		return
	}

	minor := pgMinorUpdate{}
	if running && major > 0 {
		avail, target, err := s.pg.MinorUpdateAvailable(ctx, major)
		if err != nil {
			writeError(w, err)
			return
		}
		if avail {
			minor = pgMinorUpdate{Available: true, Target: target}
		}
	}

	majors := make([]pgMajorOption, 0)
	if running && major > 0 {
		for _, mr := range pg.MajorsNewerThan(major) {
			majors = append(majors, pgMajorOption{Major: mr.Major, Default: mr.Default})
		}
	}

	var pending *pg.PendingFinalization
	st, err := s.upgrades.Load(ctx)
	if err != nil {
		writeError(w, err)
		return
	}
	pending = st.Pending

	writeData(w, http.StatusOK, pgVersionResponse{
		Running:             running,
		Current:             pgCurrentVersion{Full: full, Major: major},
		Available:           pgAvailableUpdates{Minor: minor, Majors: majors},
		PendingFinalization: pending,
	})
}

// --- GET /api/pg/upgrade/status ---

type upgradeStatusResponse struct {
	Operation *pg.OperationState      `json:"operation"`
	Pending   *pg.PendingFinalization `json:"pending_finalization"`
}

// handleUpgradeStatus returns the current operation state and pending-
// finalization state so the SPA can resume after a reload. Read-only.
func (s *Server) handleUpgradeStatus(w http.ResponseWriter, r *http.Request) {
	st, err := s.upgrades.Load(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, upgradeStatusResponse{Operation: st.Operation, Pending: st.Pending})
}

// --- POST /api/pg/upgrade/minor ---

type minorUpgradeRequest struct {
	Backup bool `json:"backup"`
}

// handleMinorUpgrade starts an asynchronous minor upgrade (§4). It runs the fast
// gates (lock, no-pending) synchronously, then hands the apt-upgrade + restart
// to a background goroutine.
func (s *Server) handleMinorUpgrade(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req minorUpgradeRequest
	if err := decodeJSON(r, &req, maxBodyBytes); err != nil {
		writeError(w, err)
		return
	}
	_, current, running, err := s.pg.CurrentVersion(ctx)
	if err != nil {
		writeError(w, err)
		return
	}
	if !running {
		writeError(w, core.ConflictError("PostgreSQL must be running to apply a minor update"))
		return
	}
	available, _, err := s.pg.MinorUpdateAvailable(ctx, current)
	if err != nil {
		writeError(w, err)
		return
	}
	if !available {
		writeError(w, core.ConflictError("no minor PostgreSQL update is available"))
		return
	}

	if !s.upgradeMu.TryLock() {
		writeError(w, errUpgradeInProgress())
		return
	}
	release := true
	defer func() {
		if release {
			s.upgradeMu.Unlock()
		}
	}()

	st, err := s.upgrades.Load(ctx)
	if err != nil {
		writeError(w, err)
		return
	}
	if st.Pending != nil {
		writeError(w, errUpgradePending())
		return
	}

	if err := s.beginUpgradeOp(pg.OpMinor, current, current, "starting minor upgrade"); err != nil {
		writeError(w, err)
		return
	}
	s.audit(ctx, "pg_minor_upgrade", "postgresql", "success", "minor upgrade started", "")

	release = false
	go s.runMinorUpgrade(req.Backup)

	s.writeUpgradeStatus(w, http.StatusAccepted)
}

// --- POST /api/pg/upgrade/major/preflight ---

type majorPreflightRequest struct {
	TargetMajor int `json:"target_major"`
}

type preflightPreview struct {
	FromMajor         int      `json:"from_major"`
	ToMajor           int      `json:"to_major"`
	DiskRequiredBytes int64    `json:"disk_required_bytes"`
	DiskFreeBytes     int64    `json:"disk_free_bytes"`
	Extensions        []string `json:"extensions"`
	Blocking          bool     `json:"blocking"`
}

type preflightResponse struct {
	Checks  pg.CheckSet      `json:"checks"`
	Preview preflightPreview `json:"preview"`
}

// handleMajorPreflight runs §5 Phase A: install the target packages and run the
// major-upgrade checklist, returning the inline {checks, preview} contract. It
// is non-destructive but installs packages and probes the cluster, so it holds
// the global lock for its duration (refusing a concurrent start) and records the
// preflight outcome for the start guard.
func (s *Server) handleMajorPreflight(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req majorPreflightRequest
	if err := decodeJSON(r, &req, maxBodyBytes); err != nil {
		writeError(w, err)
		return
	}

	if !s.upgradeMu.TryLock() {
		writeError(w, errUpgradeInProgress())
		return
	}
	defer s.upgradeMu.Unlock()

	_, current, running, err := s.pg.CurrentVersion(ctx)
	if err != nil {
		writeError(w, err)
		return
	}
	if !running {
		writeError(w, core.InternalError("PostgreSQL must be running to plan a major upgrade"))
		return
	}
	if err := validateUpgradeTarget(current, req.TargetMajor); err != nil {
		writeError(w, err)
		return
	}
	st, err := s.upgrades.Load(ctx)
	if err != nil {
		writeError(w, err)
		return
	}
	if st.Pending != nil {
		writeError(w, errUpgradePending())
		return
	}

	res, err := s.pg.MajorUpgradePreflight(ctx, current, req.TargetMajor)
	if err != nil {
		s.audit(ctx, "pg_major_preflight", fmt.Sprintf("%d->%d", current, req.TargetMajor), "failure", "preflight failed", core.CodeOf(err))
		writeError(w, err)
		return
	}

	blocking := res.Checks.HasFail()
	// Record the preflight outcome so the start endpoint can enforce the
	// "no major start without a clean (no-fail) preflight" guard.
	if err := s.mutateUpgrade(func(st *pg.UpgradeState) {
		st.LastPreflight = &pg.PreflightMemo{TargetMajor: req.TargetMajor, HasFail: blocking, At: time.Now().UTC()}
	}); err != nil {
		writeError(w, err)
		return
	}
	s.audit(ctx, "pg_major_preflight", fmt.Sprintf("%d->%d", current, req.TargetMajor), "success", "preflight completed", "")

	writeData(w, http.StatusOK, preflightResponse{
		Checks: res.Checks,
		Preview: preflightPreview{
			FromMajor:         current,
			ToMajor:           req.TargetMajor,
			DiskRequiredBytes: res.DiskRequiredBytes,
			DiskFreeBytes:     res.DiskFreeBytes,
			Extensions:        res.Extensions,
			Blocking:          blocking,
		},
	})
}

// --- POST /api/pg/upgrade/major/start ---

// maxPreflightAge bounds how long a passing preflight stays valid for starting a
// major upgrade. The cluster state the preflight inspects — prepared
// transactions, replication slots, free disk — can drift after it runs, so a
// pass older than this must be re-run before the upgrade may begin.
const maxPreflightAge = time.Hour

type majorStartRequest struct {
	TargetMajor int  `json:"target_major"`
	Confirm     bool `json:"confirm"`
}

// handleMajorUpgradeStart runs §5 Phase B asynchronously. It refuses unless the
// most recent preflight for this target had no fail, no other upgrade is in
// progress, and nothing awaits finalization.
func (s *Server) handleMajorUpgradeStart(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req majorStartRequest
	if err := decodeJSON(r, &req, maxBodyBytes); err != nil {
		writeError(w, err)
		return
	}
	if !req.Confirm {
		writeError(w, core.ValidationError("a major upgrade must be confirmed").
			WithHint("set confirm=true once you have reviewed the preflight and preview"))
		return
	}

	_, current, running, err := s.pg.CurrentVersion(ctx)
	if err != nil {
		writeError(w, err)
		return
	}
	if !running {
		writeError(w, core.InternalError("PostgreSQL must be running to start a major upgrade"))
		return
	}
	if err := validateUpgradeTarget(current, req.TargetMajor); err != nil {
		writeError(w, err)
		return
	}

	if !s.upgradeMu.TryLock() {
		writeError(w, errUpgradeInProgress())
		return
	}
	release := true
	defer func() {
		if release {
			s.upgradeMu.Unlock()
		}
	}()

	st, err := s.upgrades.Load(ctx)
	if err != nil {
		writeError(w, err)
		return
	}
	if st.Pending != nil {
		writeError(w, errUpgradePending())
		return
	}
	if st.LastPreflight == nil || st.LastPreflight.TargetMajor != req.TargetMajor || st.LastPreflight.HasFail {
		writeError(w, core.NewSafetyError(
			"major upgrade",
			[]string{"a clean (no-fail) preflight for this target major"},
			"refusing to start a major upgrade to %d without a passing preflight; run the preflight and clear all blockers first", req.TargetMajor))
		return
	}
	// A stale pass is not a pass: the blockers the preflight checks (prepared
	// transactions, replication slots, free disk) can change after it ran.
	if age := time.Since(st.LastPreflight.At); age > maxPreflightAge {
		writeError(w, core.NewSafetyError(
			"major upgrade",
			[]string{fmt.Sprintf("a preflight run within the last %s", maxPreflightAge)},
			"the preflight for PostgreSQL %d ran %s ago; re-run it so the checks reflect your database's current state",
			req.TargetMajor, age.Round(time.Minute)))
		return
	}

	if err := s.beginUpgradeOp(pg.OpMajor, current, req.TargetMajor, "starting major upgrade"); err != nil {
		writeError(w, err)
		return
	}
	s.audit(ctx, "pg_major_upgrade", fmt.Sprintf("%d->%d", current, req.TargetMajor), "success", "major upgrade started", "")

	release = false
	go s.runMajorUpgrade(current, req.TargetMajor)

	s.writeUpgradeStatus(w, http.StatusAccepted)
}

// --- POST /api/pg/upgrade/finalize ---

type finalizeRequest struct {
	ConfirmVersion int `json:"confirm_version"`
}

// handleUpgradeFinalize drops the old cluster (the point of no return), gated by
// a typed-version confirmation that must match the old major.
func (s *Server) handleUpgradeFinalize(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req finalizeRequest
	if err := decodeJSON(r, &req, maxBodyBytes); err != nil {
		writeError(w, err)
		return
	}

	if !s.upgradeMu.TryLock() {
		writeError(w, errUpgradeInProgress())
		return
	}
	release := true
	defer func() {
		if release {
			s.upgradeMu.Unlock()
		}
	}()

	st, err := s.upgrades.Load(ctx)
	if err != nil {
		writeError(w, err)
		return
	}
	if st.Pending == nil {
		writeError(w, core.ConflictError("no upgrade is awaiting finalization"))
		return
	}
	if req.ConfirmVersion != st.Pending.FromMajor {
		writeError(w, core.NewSafetyError(
			"finalize upgrade",
			[]string{fmt.Sprintf("confirm_version=%d", st.Pending.FromMajor)},
			"finalize requires confirming the old major (%d) to drop its cluster", st.Pending.FromMajor))
		return
	}
	_, runningMajor, running, err := s.pg.CurrentVersion(ctx)
	if err != nil {
		writeError(w, err)
		return
	}
	if !running || runningMajor != st.Pending.ToMajor {
		writeError(w, core.ConflictError(
			"refusing to delete PostgreSQL %d while PostgreSQL %d is not the confirmed live cluster",
			st.Pending.FromMajor, st.Pending.ToMajor))
		return
	}

	oldMajor := st.Pending.FromMajor
	if err := s.beginUpgradeOp(pg.OpFinalize, oldMajor, st.Pending.ToMajor, "finalizing upgrade"); err != nil {
		writeError(w, err)
		return
	}
	s.audit(ctx, "pg_upgrade_finalize", fmt.Sprintf("drop %d", oldMajor), "success", "finalize started", "")

	release = false
	go s.runFinalize(oldMajor)

	s.writeUpgradeStatus(w, http.StatusAccepted)
}

// --- POST /api/pg/upgrade/rollback ---

type rollbackRequest struct {
	ConfirmVersion int `json:"confirm_version"`
}

// handleUpgradeRollback returns the box to the old major. The SPA's confirm
// dialog must warn that writes made against the new major during the
// verification window are discarded.
func (s *Server) handleUpgradeRollback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req rollbackRequest
	if err := decodeJSON(r, &req, maxBodyBytes); err != nil {
		writeError(w, err)
		return
	}
	if !s.upgradeMu.TryLock() {
		writeError(w, errUpgradeInProgress())
		return
	}
	release := true
	defer func() {
		if release {
			s.upgradeMu.Unlock()
		}
	}()

	st, err := s.upgrades.Load(ctx)
	if err != nil {
		writeError(w, err)
		return
	}
	if st.Pending == nil {
		writeError(w, core.ConflictError("no upgrade is awaiting finalization to roll back"))
		return
	}
	if req.ConfirmVersion != st.Pending.ToMajor {
		writeError(w, core.NewSafetyError(
			"rollback upgrade",
			[]string{fmt.Sprintf("confirm_version=%d", st.Pending.ToMajor)},
			"rollback requires confirming the live major (%d) whose post-upgrade writes will be discarded", st.Pending.ToMajor))
		return
	}

	fromMajor, toMajor, oldPort := st.Pending.FromMajor, st.Pending.ToMajor, st.OldClusterPort
	if err := s.beginUpgradeOp(pg.OpRollback, fromMajor, toMajor, "rolling back to the old major"); err != nil {
		writeError(w, err)
		return
	}
	s.audit(ctx, "pg_upgrade_rollback", fmt.Sprintf("%d<-%d", fromMajor, toMajor), "success", "rollback started", "")

	release = false
	go s.runRollback(fromMajor, toMajor, oldPort)

	s.writeUpgradeStatus(w, http.StatusAccepted)
}

// --- async workers ---

// runMinorUpgrade is the background worker for a minor upgrade. It owns the
// global lock until it returns. The phase trail is persisted so the UI can poll.
func (s *Server) runMinorUpgrade(takeBackup bool) {
	ctx, cancel := upgradeWorkerContext()
	defer cancel()
	defer s.upgradeMu.Unlock()

	if takeBackup {
		s.upgradePhase("backup", "taking a pre-upgrade backup")
		if err := s.upgradeBackup(ctx); err != nil {
			s.failUpgradeOp(core.InternalError("pre-upgrade backup failed; minor upgrade aborted").Wrap(err))
			return
		}
		s.appendUpgradeLog("pre-upgrade pgBackRest full backup completed")
	}

	s.upgradePhase("upgrade", "applying the minor package upgrade and restarting Postgres")
	res, err := s.pg.MinorUpgrade(ctx)
	if err != nil {
		s.failUpgradeOp(err)
		return
	}
	s.appendUpgradeLog(res.Statements...)
	msg := "minor upgrade complete"
	if v, ok := res.Data["version"].(string); ok && v != "" {
		msg = "now on " + v
	}
	s.succeedUpgradeOp(msg)
}

// runMajorUpgrade is the background worker for §5 Phase B: mandatory backup,
// pg_upgradecluster, re-apply managed config, rebuild stats, update extensions,
// smoke test, then land in pending-finalization. On any failure the old cluster
// is preserved by design and the operation is recorded failed.
func (s *Server) runMajorUpgrade(fromMajor, toMajor int) {
	ctx, cancel := upgradeWorkerContext()
	defer cancel()
	defer s.upgradeMu.Unlock()

	// 1. Mandatory pre-upgrade backup — a hard gate.
	s.upgradePhase("backup", "taking the mandatory pre-upgrade backup")
	if err := s.upgradeBackup(ctx); err != nil {
		s.failUpgradeOp(core.InternalError("mandatory pre-upgrade backup failed; upgrade aborted (nothing was changed)").Wrap(err))
		return
	}
	s.appendUpgradeLog("mandatory pgBackRest full backup completed")

	// 2. pg_upgradecluster (copy). The old cluster is preserved on any failure.
	s.upgradePhase("upgrade", "running pg_upgradecluster (this is the downtime window)")
	up, err := s.pg.UpgradeCluster(ctx, fromMajor, toMajor)
	if err != nil {
		// A non-empty step means pg_upgradecluster itself succeeded and only the
		// rollback metadata read failed. Persist the two-cluster state before
		// surfacing the failure so finalize/recovery remains visible after restart.
		if len(up.Steps) > 0 {
			if persistErr := s.persistPendingUpgrade(fromMajor, toMajor, up, time.Now().UTC()); persistErr != nil {
				err = core.InternalError("upgrade completed but neither rollback metadata nor recovery state could be persisted").
					WithDetail("metadata_error", err.Error()).Wrap(persistErr)
			}
		}
		s.failUpgradeOp(err)
		return
	}
	s.appendUpgradeLog(up.Steps...)

	// Record the pending-finalization + rollback metadata the instant the new
	// cluster is live and the old one is parked — BEFORE any later step that could
	// fail (C-1). pg_upgradecluster has already created the rollback situation; if
	// the reconnect, reconfigure, or smoke test then fails, the operator must
	// still be able to roll back or finalize from the panel rather than be
	// stranded with two clusters and no UI. ReclaimableBytes is filled in below
	// once the upgrade verifies.
	upgradedAt := time.Now().UTC()
	if err := s.persistPendingUpgrade(fromMajor, toMajor, up, upgradedAt); err != nil {
		// Continuing while the rollback coordinates are not durable would strand
		// the operator after a panel restart. Revert immediately, before any
		// post-upgrade app writes can accumulate.
		_, rollbackErr := s.pg.RollbackUpgrade(ctx, fromMajor, toMajor, up.OldPort)
		if rollbackErr != nil {
			s.failUpgradeOp(core.InternalError("could not persist upgrade recovery state and automatic rollback failed").
				WithDetail("state_error", err.Error()).WithDetail("rollback_error", rollbackErr.Error()))
			return
		}
		s.failUpgradeOp(core.InternalError("could not persist upgrade recovery state; the upgrade was automatically rolled back").Wrap(err))
		return
	}

	// The new cluster is now live on the original port; reconnect the pools to it.
	s.pg.Close()
	if cerr := s.pg.Connect(ctx); cerr != nil {
		s.log.Warn("could not reconnect to the upgraded cluster", "err", cerr)
	}

	// 3. Re-apply panel-managed config (socket auth, archiving, tuning).
	s.upgradePhase("reconfigure", "re-applying panel-managed configuration")
	cfg, cfgErr := config.Load(ctx, s.store)
	if cfgErr != nil {
		s.failUpgradeOp(cfgErr)
		return
	}
	if steps, rerr := s.pg.ReapplyManagedConfig(ctx, cfg.Stanza); rerr != nil {
		s.failUpgradeOp(rerr)
		return
	} else {
		s.appendUpgradeLog(steps...)
	}

	// 4. Rebuild planner statistics (not carried by pg_upgrade). Non-fatal.
	s.upgradePhase("analyze", "rebuilding planner statistics (vacuumdb --analyze-in-stages)")
	if step, verr := s.pg.VacuumAnalyzeAll(ctx); verr != nil {
		s.appendUpgradeLog("vacuumdb failed (run it manually): " + verr.Error())
	} else {
		s.appendUpgradeLog(step)
	}

	// 5. Update extensions to the new major. Non-fatal.
	s.upgradePhase("extensions", "updating extensions to the new major")
	if steps, eerr := s.pg.UpdateAllExtensions(ctx); eerr != nil {
		s.appendUpgradeLog("extension update scan failed: " + eerr.Error())
	} else {
		s.appendUpgradeLog(steps...)
	}

	// 6. Smoke test.
	s.upgradePhase("smoke", "running the post-upgrade smoke test")
	if serr := s.pg.SmokeTest(ctx, toMajor); serr != nil {
		s.failUpgradeOp(serr)
		return
	}

	// 7. Now that the upgrade is verified, fill in the reclaimable figure on the
	// pending state that was recorded right after pg_upgradecluster.
	if up.OldDataDir != "" {
		if reclaim, derr := s.pg.DirSizeBytes(ctx, up.OldDataDir); derr == nil && reclaim > 0 {
			s.mutateUpgrade(func(st *pg.UpgradeState) {
				if st.Pending != nil {
					st.Pending.ReclaimableBytes = reclaim
				}
			})
		}
	}
	s.succeedUpgradeOp(fmt.Sprintf("upgraded to PostgreSQL %d — verify, then finalize to reclaim space or roll back", toMajor))
}

// runFinalize drops the old cluster and clears the pending state.
func (s *Server) runFinalize(oldMajor int) {
	ctx, cancel := upgradeWorkerContext()
	defer cancel()
	defer s.upgradeMu.Unlock()

	s.upgradePhase("finalize", "dropping the old cluster")
	steps, err := s.pg.FinalizeUpgrade(ctx, oldMajor)
	if err != nil {
		s.failUpgradeOp(err)
		return
	}
	s.appendUpgradeLog(steps...)
	if err := s.clearPending(); err != nil {
		s.failUpgradeOp(core.InternalError("old cluster was dropped but finalization state could not be cleared; retry finalize").Wrap(err))
		return
	}
	s.succeedUpgradeOp("old cluster dropped; disk reclaimed")
}

// runRollback returns the box to the old major and clears the pending state.
func (s *Server) runRollback(fromMajor, toMajor int, oldPort string) {
	ctx, cancel := upgradeWorkerContext()
	defer cancel()
	defer s.upgradeMu.Unlock()

	s.upgradePhase("rollback", "stopping the new cluster and restoring the old one on the live port")
	steps, err := s.pg.RollbackUpgrade(ctx, fromMajor, toMajor, oldPort)
	if err != nil {
		s.failUpgradeOp(err)
		return
	}
	s.appendUpgradeLog(steps...)

	// The old cluster is live again on the original port; reconnect the pools.
	s.pg.Close()
	if cerr := s.pg.Connect(ctx); cerr != nil {
		s.log.Warn("could not reconnect to the rolled-back cluster", "err", cerr)
	}

	if err := s.clearPending(); err != nil {
		s.failUpgradeOp(core.InternalError("old cluster was restored but rollback state could not be cleared; retry rollback").Wrap(err))
		return
	}
	s.succeedUpgradeOp(fmt.Sprintf("rolled back to PostgreSQL %d", fromMajor))
}

// upgradeBackup runs the synchronous pre-upgrade pgBackRest full backup, reusing
// the same config self-heal + Backup entrypoint the backups page uses. It is the
// mandatory gate for a major upgrade and the optional one for a minor upgrade.
func (s *Server) upgradeBackup(ctx context.Context) error {
	cfg, err := config.Load(ctx, s.store)
	if err != nil {
		return err
	}
	if _, err := s.ensureBackupConfigured(ctx, cfg); err != nil {
		return err
	}
	if _, err := s.backups.Backup(ctx, backup.TypeFull); err != nil {
		return err
	}
	return nil
}

const upgradeJobTimeout = 24 * time.Hour

func upgradeWorkerContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), upgradeJobTimeout)
}

// --- operation-state helpers (durable, mutex-serialized) ---

// beginUpgradeOp synchronously records a fresh running operation so the 202
// response and the first poll both see it before the goroutine advances it.
func (s *Server) beginUpgradeOp(kind string, from, target int, msg string) error {
	now := time.Now().UTC()
	return s.mutateUpgrade(func(st *pg.UpgradeState) {
		st.Operation = &pg.OperationState{
			Kind:        kind,
			Status:      pg.OpStatusRunning,
			Phase:       "starting",
			Message:     msg,
			FromMajor:   from,
			TargetMajor: target,
			StartedAt:   now,
			Log:         []string{},
		}
	})
}

func (s *Server) persistPendingUpgrade(fromMajor, toMajor int, up pg.UpgradeClusterResult, upgradedAt time.Time) error {
	return s.mutateUpgrade(func(st *pg.UpgradeState) {
		st.Pending = &pg.PendingFinalization{
			FromMajor:  fromMajor,
			ToMajor:    toMajor,
			UpgradedAt: upgradedAt,
		}
		st.OldClusterPort = up.OldPort
		st.OldDataDir = up.OldDataDir
	})
}

func (s *Server) upgradePhase(phase, msg string) {
	s.mutateUpgrade(func(st *pg.UpgradeState) {
		if st.Operation != nil {
			st.Operation.Phase = phase
			st.Operation.Message = msg
		}
	})
}

func (s *Server) appendUpgradeLog(lines ...string) {
	if len(lines) == 0 {
		return
	}
	s.mutateUpgrade(func(st *pg.UpgradeState) {
		if st.Operation != nil {
			st.Operation.Log = append(st.Operation.Log, lines...)
		}
	})
}

func (s *Server) failUpgradeOp(err error) {
	now := time.Now().UTC()
	s.mutateUpgrade(func(st *pg.UpgradeState) {
		if st.Operation != nil {
			st.Operation.Status = pg.OpStatusFailed
			st.Operation.Error = err.Error()
			st.Operation.FinishedAt = &now
		}
	})
	s.log.Warn("pg upgrade operation failed", "err", err)
}

func (s *Server) succeedUpgradeOp(msg string) {
	now := time.Now().UTC()
	s.mutateUpgrade(func(st *pg.UpgradeState) {
		if st.Operation != nil {
			st.Operation.Status = pg.OpStatusSuccess
			st.Operation.Phase = "done"
			st.Operation.Message = msg
			st.Operation.FinishedAt = &now
		}
	})
}

// clearPending wipes the pending-finalization + rollback metadata + preflight
// memo, used after a finalize or rollback resolves the two clusters down to one.
func (s *Server) clearPending() error {
	return s.mutateUpgrade(func(st *pg.UpgradeState) {
		st.Pending = nil
		st.OldClusterPort = ""
		st.OldDataDir = ""
		st.LastPreflight = nil
	})
}

// mutateUpgrade applies fn to the durable upgrade state on a detached context so
// a terminal write is not lost to a cancelled worker context.
func (s *Server) mutateUpgrade(fn func(*pg.UpgradeState)) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := s.upgrades.Mutate(ctx, fn); err != nil {
		s.log.Warn("could not persist upgrade state", "err", err)
		return err
	}
	return nil
}

// writeUpgradeStatus loads and writes the current status document with the given
// HTTP status (the immediate ack for a started operation).
func (s *Server) writeUpgradeStatus(w http.ResponseWriter, status int) {
	st, err := s.upgrades.Load(context.Background())
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, status, upgradeStatusResponse{Operation: st.Operation, Pending: st.Pending})
}

// sweepInterruptedUpgrade marks a "running" upgrade operation as failed on panel
// startup: its owning goroutine died with the previous process, so the row would
// otherwise show a phantom in-flight upgrade forever (mirrors the backup/
// migration sweeps). The pending-finalization state is deliberately left intact
// so the banner reappears after a restart mid-window.
func (s *Server) sweepInterruptedUpgrade(ctx context.Context) {
	st, err := s.upgrades.Load(ctx)
	if err != nil {
		s.log.Warn("could not read upgrade state on startup", "err", err)
		return
	}
	if st.Operation != nil && st.Operation.Status == pg.OpStatusRunning {
		now := time.Now().UTC()
		st.Operation.Status = pg.OpStatusFailed
		st.Operation.Error = "interrupted by panel restart"
		st.Operation.FinishedAt = &now
		if serr := s.upgrades.Save(ctx, st); serr != nil {
			s.log.Warn("could not sweep interrupted upgrade on startup", "err", serr)
			return
		}
		s.log.Warn("marked interrupted upgrade operation as failed on startup")
	}
}

// validateUpgradeTarget checks a requested major-upgrade target is supported and
// strictly newer than the current major.
func validateUpgradeTarget(current, target int) error {
	if !pg.IsSupported(target) {
		return core.ValidationError("PostgreSQL %d is not a supported upgrade target", target).
			WithHint("choose a supported major from the version catalog")
	}
	if current <= 0 {
		return core.InternalError("could not read the current PostgreSQL major version")
	}
	if target <= current {
		return core.ValidationError("target major %d must be newer than the current major %d", target, current)
	}
	return nil
}

// errUpgradeInProgress is the typed refusal when an upgrade operation already
// holds the global lock.
func errUpgradeInProgress() error {
	return core.ConflictError("an upgrade operation is already in progress").
		WithHint("wait for the current upgrade operation to finish before starting another")
}

// errUpgradePending is the typed refusal when the box awaits finalization.
func errUpgradePending() error {
	return core.ConflictError("an upgrade is awaiting finalization").
		WithHint("finalize (reclaim disk) or roll back the pending upgrade before starting another")
}
