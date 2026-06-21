package migrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/venkatesh-sekar/indiepg/internal/core"
)

// Phase is the finer-grained step within a migration that the UI surfaces in
// addition to the coarse Status. Because pg_dump/pg_restore give no clean
// progress percent, the phase plus elapsed time is the primary signal the panel
// shows while a job runs.
type Phase string

const (
	// PhaseValidating is the pre-flight: source reachable, tools present, and the
	// overwrite-safety gate on a non-empty target.
	PhaseValidating Phase = "validating"
	// PhaseDumping means pg_dump (or pg_dumpall -g) is running against the source.
	PhaseDumping Phase = "dumping"
	// PhaseUploading means the dump is being written to the shared S3 bucket
	// (ssh-less SOURCE side only).
	PhaseUploading Phase = "uploading"
	// PhaseDownloading means the dump is being fetched from the shared S3 bucket
	// (ssh-less TARGET side only).
	PhaseDownloading Phase = "downloading"
	// PhaseRestoring means pg_restore is running against the local target.
	PhaseRestoring Phase = "restoring"
	// PhaseVerifying means source and target row counts are being compared.
	PhaseVerifying Phase = "verifying"
)

// Recorder is the local-store sink the orchestrator writes its progress to. It
// is the SOURCE OF TRUTH for this panel's view of a migration (status, phase,
// progress, errors, row counts). The migrate package deliberately does NOT
// import internal/store; the server provides a small adapter over
// (*store.Store, id) that satisfies this interface.
//
// Stage/Progress are advisory: an error from them is logged by the orchestrator
// but never masks the migration outcome. Fail/Succeed are the terminal sinks.
type Recorder interface {
	// Stage records a status + phase boundary (e.g. status=importing phase=dumping).
	Stage(ctx context.Context, status Status, phase Phase) error
	// Progress records a coarse done/total step count plus the total bytes dumped
	// so far. For single-db, total is 1; for cluster, total is the database count.
	Progress(ctx context.Context, done, total, bytesTotal int64) error
	// Fail marks the migration failed with the (already scrubbed) cause and a
	// finished timestamp.
	Fail(ctx context.Context, cause error) error
	// Succeed marks the migration completed with the verified row counts and a
	// finished timestamp.
	Succeed(ctx context.Context, src, tgt map[string]int64) error
}

// Job describes one DIRECT-PULL migration the orchestrator runs end to end.
// Source and Target are ConnInfo values; for direct pull the Target is always
// the panel's own local Postgres (Target.Local() == true).
//
// Note: the per-job temp directory lives on the Orchestrator (set via
// NewOrchestrator's workDir), not on the Job, so it is shared by the direct and
// ssh-less session entry points alike.
type Job struct {
	Mode           Mode
	Source         ConnInfo
	Target         ConnInfo
	TargetDatabase string
	Overwrite      bool
	// Exclude lists tables to skip in the dump (single-db) — passed through to
	// DumpOpts.ExcludeTables.
	Exclude []string
}

// Orchestrator runs the actual data movement for a migration job. It composes
// the PgEngine (dump/restore/psql), the session Service + ObjectStore (only the
// ssh-less mode needs S3), and a Recorder (the local-store progress sink),
// writing all dumps under a per-job temp directory it cleans up on return.
//
// One Orchestrator is built per request by the server with a fresh workDir.
type Orchestrator struct {
	engine  PgEngine
	svc     *Service
	os      ObjectStore
	log     *core.Logger
	workDir string
}

// NewOrchestrator builds an Orchestrator.
//
// FINAL SIGNATURE (server wiring depends on this):
//
//		func NewOrchestrator(engine PgEngine, svc *Service, os ObjectStore, workDir string, log *core.Logger) *Orchestrator
//
//	  - engine is required.
//	  - svc and os are used ONLY by the ssh-less session methods
//	    (ExportToSession/ImportFromSession) and may be nil for direct-only use;
//	    the Direct path never touches S3.
//	  - workDir is the per-job temp directory (the server creates it 0700); all
//	    dumps are written under it and it is removed on return. May be "" only in
//	    tests that stub the engine so no file is ever written.
//	  - log is nil-safe (falls back to core.Discard()).
func NewOrchestrator(engine PgEngine, svc *Service, os ObjectStore, workDir string, log *core.Logger) *Orchestrator {
	if log == nil {
		log = core.Discard()
	}
	return &Orchestrator{engine: engine, svc: svc, os: os, workDir: workDir, log: log}
}

// Direct runs a DIRECT-PULL migration (no S3): the panel dumps from a
// user-supplied source and restores into its own local Postgres. It dispatches
// on job.Mode to the single-database or whole-cluster flow. On any error it
// records the (scrubbed) failure via rec.Fail and returns that error; on success
// it calls rec.Succeed. The workDir is always cleaned up on return.
func (o *Orchestrator) Direct(ctx context.Context, job Job, rec Recorder) error {
	defer o.cleanup()
	switch job.Mode {
	case ModeSingleDB:
		return o.directSingle(ctx, job, rec)
	case ModeCluster:
		return o.directCluster(ctx, job, rec)
	default:
		return o.fail(ctx, rec, core.ValidationError("unsupported direct migration mode %q", job.Mode))
	}
}

// directSingle pulls one database: validate -> dump -> restore -> verify.
func (o *Orchestrator) directSingle(ctx context.Context, job Job, rec Recorder) error {
	// --- validating ------------------------------------------------------
	o.stage(ctx, rec, StatusImporting, PhaseValidating)
	if err := o.validateSource(ctx, job.Source); err != nil {
		return o.fail(ctx, rec, err)
	}
	if err := o.validateTargetOverwrite(ctx, job); err != nil {
		return o.fail(ctx, rec, err)
	}

	// --- dumping ---------------------------------------------------------
	o.stage(ctx, rec, StatusImporting, PhaseDumping)
	_ = rec.Progress(ctx, 0, 1, 0)
	dumpPath := filepath.Join(o.workDir, "single.dump")
	info, err := o.engine.Dump(ctx, job.Source, job.Source.Database, dumpPath, DumpOpts{ExcludeTables: job.Exclude})
	if err != nil {
		return o.fail(ctx, rec, err)
	}
	_ = rec.Progress(ctx, 0, 1, info.SizeBytes)

	// --- restoring -------------------------------------------------------
	o.stage(ctx, rec, StatusImporting, PhaseRestoring)
	if err := o.prepareTarget(ctx, job); err != nil {
		return o.fail(ctx, rec, err)
	}
	if err := o.engine.Restore(ctx, job.Target, dumpPath, job.TargetDatabase, RestoreOpts{
		Clean:   job.Overwrite,
		NoOwner: true,
	}); err != nil {
		return o.fail(ctx, rec, err)
	}
	_ = rec.Progress(ctx, 1, 1, info.SizeBytes)

	// --- verifying -------------------------------------------------------
	o.stage(ctx, rec, StatusImporting, PhaseVerifying)
	src, tgt, err := o.verify(ctx, job.Source, job.Source.Database, job.Target, job.TargetDatabase)
	if err != nil {
		return o.fail(ctx, rec, err)
	}
	if err := o.succeed(ctx, rec, src, tgt); err != nil {
		return o.fail(ctx, rec, err)
	}
	return nil
}

// directCluster moves every non-template source database plus globals:
// validate -> dump globals -> per-db dump+restore loop -> verify each -> succeed.
//
// Globals (roles/grants/tablespaces) are captured with pg_dumpall -g into the
// workDir and replayed into the local target via RestoreGlobals BEFORE any
// per-database restore, so the roles and grants the per-db archives reference
// actually exist on the target. Per-database archives are then restored with
// --create and --no-owner so a benign mismatch degrades to a warning rather than
// aborting the whole cluster move.
//
// Safety: when Overwrite is NOT set, each target database that already holds user
// tables is a hard stop (a typed-name SafetyError) so an existing database is
// never silently written on top of. When Overwrite IS set, the handler has
// already required the typed sentinel confirmation, and each target is dropped
// for a clean slate.
func (o *Orchestrator) directCluster(ctx context.Context, job Job, rec Recorder) error {
	// --- validating ------------------------------------------------------
	o.stage(ctx, rec, StatusImporting, PhaseValidating)
	if err := o.validateSource(ctx, job.Source); err != nil {
		return o.fail(ctx, rec, err)
	}
	dbs, err := o.engine.ListDatabases(ctx, job.Source)
	if err != nil {
		return o.fail(ctx, rec, err)
	}
	total := int64(len(dbs))
	_ = rec.Progress(ctx, 0, total, 0)

	// --- globals first ---------------------------------------------------
	o.stage(ctx, rec, StatusImporting, PhaseDumping)
	globalsPath := filepath.Join(o.workDir, "globals.sql")
	if err := o.engine.DumpGlobals(ctx, job.Source, globalsPath); err != nil {
		return o.fail(ctx, rec, err)
	}
	// Replay globals into the local target BEFORE per-database restores so the
	// roles/grants the archives reference exist. Without this, --no-owner masks
	// missing roles and source ownership/GRANTs would be silently dropped.
	o.stage(ctx, rec, StatusImporting, PhaseRestoring)
	if err := o.engine.RestoreGlobals(ctx, job.Target, globalsPath); err != nil {
		return o.fail(ctx, rec, err)
	}

	// --- per-database loop ----------------------------------------------
	var bytesTotal int64
	mergedSrc := map[string]int64{}
	mergedTgt := map[string]int64{}
	for i, db := range dbs {
		// Safety: refuse to write over a non-empty target database unless the
		// operator opted into overwrite (the handler already required the typed
		// sentinel confirmation for that). Mirrors directSingle's gate per database.
		if !job.Overwrite {
			nonEmpty, err := o.engine.DatabaseNonEmpty(ctx, job.Target, db.Name)
			if err != nil {
				return o.fail(ctx, rec, err)
			}
			if nonEmpty {
				return o.fail(ctx, rec, core.RequireConfirmation(
					"overwrite database "+db.Name, db.Name, ""))
			}
		}

		o.stage(ctx, rec, StatusImporting, PhaseDumping)
		dumpPath := filepath.Join(o.workDir, "db_"+strconv.Itoa(i)+".dump")
		info, err := o.engine.Dump(ctx, job.Source, db.Name, dumpPath, DumpOpts{})
		if err != nil {
			return o.fail(ctx, rec, err)
		}
		bytesTotal += info.SizeBytes
		_ = rec.Progress(ctx, int64(i), total, bytesTotal)

		o.stage(ctx, rec, StatusImporting, PhaseRestoring)
		if job.Overwrite {
			if err := o.engine.DropDatabase(ctx, job.Target, db.Name); err != nil {
				return o.fail(ctx, rec, err)
			}
		}
		// --create makes pg_restore issue CREATE DATABASE from the archive.
		if err := o.engine.Restore(ctx, job.Target, dumpPath, "postgres", RestoreOpts{
			Create:  true,
			NoOwner: true,
		}); err != nil {
			return o.fail(ctx, rec, err)
		}

		// Bound disk use across a large cluster; the workDir is also removed
		// wholesale on return.
		_ = os.Remove(dumpPath)
		_ = rec.Progress(ctx, int64(i+1), total, bytesTotal)
	}

	// --- verifying -------------------------------------------------------
	o.stage(ctx, rec, StatusImporting, PhaseVerifying)
	for _, db := range dbs {
		src, tgt, err := o.verify(ctx, job.Source, db.Name, job.Target, db.Name)
		if err != nil {
			return o.fail(ctx, rec, err)
		}
		mergeCounts(mergedSrc, db.Name, src)
		mergeCounts(mergedTgt, db.Name, tgt)
	}
	if err := o.succeed(ctx, rec, mergedSrc, mergedTgt); err != nil {
		return o.fail(ctx, rec, err)
	}
	return nil
}

// ExportToSession is the SOURCE side of an ssh-less migration: it dumps the
// session's database locally, uploads the dump to the shared S3 bucket, records
// the dump size/checksum/source row counts on the session document, and
// transitions the session waiting -> exporting -> exported. The dump is written
// under workDir and cleaned up on return.
//
// Requires the ObjectStore (S3); if the orchestrator was built without one this
// returns a validation error via rec.Fail.
func (o *Orchestrator) ExportToSession(ctx context.Context, sess *MigrationSession, src ConnInfo, rec Recorder) error {
	defer o.cleanup()
	if o.os == nil || o.svc == nil {
		return o.fail(ctx, rec, core.ValidationError("ssh-less export requires S3 object storage"))
	}
	if sess == nil {
		return o.fail(ctx, rec, core.ValidationError("session is nil"))
	}

	// --- validating ------------------------------------------------------
	o.stage(ctx, rec, StatusExporting, PhaseValidating)
	if err := sess.ValidateForExport(time.Now().UTC()); err != nil {
		return o.fail(ctx, rec, err)
	}
	if err := o.validateSource(ctx, src); err != nil {
		return o.fail(ctx, rec, err)
	}
	if err := o.svc.Transition(ctx, sess, StatusExporting); err != nil {
		return o.fail(ctx, rec, err)
	}

	// --- dumping ---------------------------------------------------------
	// From here on the session is already in StatusExporting, so every off-ramp
	// must move it to StatusFailed (via failSession) so the source panel does not
	// leave the cross-panel channel stuck in "exporting".
	o.stage(ctx, rec, StatusExporting, PhaseDumping)
	dumpPath := filepath.Join(o.workDir, "export.dump")
	info, err := o.engine.Dump(ctx, src, sess.Database, dumpPath, DumpOpts{})
	if err != nil {
		return o.failSession(ctx, sess, rec, err)
	}
	_ = rec.Progress(ctx, 0, 1, info.SizeBytes)

	// Capture source row counts now, while we hold the source connection, so the
	// target can verify after restore without ever talking to the source.
	srcCounts, err := o.engine.RowCounts(ctx, src, sess.Database)
	if err != nil {
		return o.failSession(ctx, sess, rec, err)
	}

	// --- uploading -------------------------------------------------------
	o.stage(ctx, rec, StatusExporting, PhaseUploading)
	data, err := os.ReadFile(dumpPath)
	if err != nil {
		return o.failSession(ctx, sess, rec, core.InternalError("cannot read dump for upload").Wrap(err))
	}
	if err := o.os.PutObject(ctx, DumpKey(sess.Code), data); err != nil {
		return o.failSession(ctx, sess, rec, core.InternalError("failed to upload dump for session %s", sess.Code).Wrap(err))
	}

	// Record dump metadata on the session so the target can verify the download.
	sess.DumpKey = DumpKey(sess.Code)
	sess.DumpSize = info.SizeBytes
	sess.DumpChecksum = info.Checksum
	sess.SourceRowCounts = srcCounts
	if err := o.svc.UpdateSession(ctx, sess); err != nil {
		return o.failSession(ctx, sess, rec, err)
	}
	if err := o.svc.Transition(ctx, sess, StatusExported); err != nil {
		return o.failSession(ctx, sess, rec, err)
	}

	_ = rec.Progress(ctx, 1, 1, info.SizeBytes)
	// From this panel's local-record perspective the export work is done once the
	// dump is uploaded and the session is exported; the target restores+verifies
	// against its own record. Record the source counts on both sides so this
	// panel's history shows what it exported.
	if err := o.succeed(ctx, rec, srcCounts, srcCounts); err != nil {
		return o.fail(ctx, rec, err)
	}
	return nil
}

// ImportFromSession is the TARGET side of an ssh-less migration: it downloads
// the dump the source uploaded, verifies its checksum against the session
// document, restores it into the target database, captures target row counts,
// compares them to the source counts the session recorded, and transitions the
// session importing -> completed. The downloaded dump is written under workDir
// and cleaned up on return.
func (o *Orchestrator) ImportFromSession(ctx context.Context, sess *MigrationSession, tgt ConnInfo, rec Recorder) error {
	defer o.cleanup()
	if o.os == nil || o.svc == nil {
		return o.fail(ctx, rec, core.ValidationError("ssh-less import requires S3 object storage"))
	}
	if sess == nil {
		return o.fail(ctx, rec, core.ValidationError("session is nil"))
	}

	// --- validating ------------------------------------------------------
	o.stage(ctx, rec, StatusImporting, PhaseValidating)
	if err := sess.ValidateForImport(); err != nil {
		return o.fail(ctx, rec, err)
	}
	if err := o.svc.Transition(ctx, sess, StatusImporting); err != nil {
		return o.fail(ctx, rec, err)
	}

	// From here on the session is already in StatusImporting, so every off-ramp
	// must move it to StatusFailed (via failSession) so the source panel observing
	// the cross-panel channel sees the failure rather than a session stuck in
	// "importing".

	// --- downloading -----------------------------------------------------
	o.stage(ctx, rec, StatusImporting, PhaseDownloading)
	data, err := o.os.GetObject(ctx, sess.DumpKey)
	if err != nil {
		return o.failSession(ctx, sess, rec, core.InternalError("failed to download dump for session %s", sess.Code).Wrap(err))
	}
	got := sha256Hex(data)
	if got != sess.DumpChecksum {
		return o.failSession(ctx, sess, rec, core.ValidationError(
			"dump checksum mismatch for session %s (got %s, expected %s)", sess.Code, got, sess.DumpChecksum).
			WithHint("the uploaded dump is corrupt or incomplete; re-run the export"))
	}
	dumpPath := filepath.Join(o.workDir, "import.dump")
	if err := os.WriteFile(dumpPath, data, 0o600); err != nil {
		return o.failSession(ctx, sess, rec, core.InternalError("cannot stage downloaded dump").Wrap(err))
	}
	_ = rec.Progress(ctx, 0, 1, int64(len(data)))

	// --- restoring -------------------------------------------------------
	o.stage(ctx, rec, StatusImporting, PhaseRestoring)
	targetDB := sess.Database
	nonEmpty, err := o.engine.DatabaseNonEmpty(ctx, tgt, targetDB)
	if err != nil {
		return o.failSession(ctx, sess, rec, err)
	}
	if nonEmpty {
		// The target side of an ssh-less session expects a fresh database; if it
		// already holds tables that is a safety stop (no silent overwrite).
		return o.failSession(ctx, sess, rec, core.RequireConfirmation(
			"overwrite database "+targetDB, targetDB, ""))
	}
	exists, err := o.engine.DatabaseExists(ctx, tgt, targetDB)
	if err != nil {
		return o.failSession(ctx, sess, rec, err)
	}
	if !exists {
		if err := o.engine.CreateDatabase(ctx, tgt, targetDB, ""); err != nil {
			return o.failSession(ctx, sess, rec, err)
		}
	}
	if err := o.engine.Restore(ctx, tgt, dumpPath, targetDB, RestoreOpts{NoOwner: true}); err != nil {
		return o.failSession(ctx, sess, rec, err)
	}
	_ = rec.Progress(ctx, 1, 1, int64(len(data)))

	// --- verifying -------------------------------------------------------
	o.stage(ctx, rec, StatusImporting, PhaseVerifying)
	tgtCounts, err := o.engine.RowCounts(ctx, tgt, targetDB)
	if err != nil {
		return o.failSession(ctx, sess, rec, err)
	}
	if diffs := CompareRowCounts(sess.SourceRowCounts, tgtCounts); len(diffs) > 0 {
		return o.failSession(ctx, sess, rec, rowMismatchError(diffs))
	}

	// Record target counts on the session and finish it.
	sess.TargetRowCounts = tgtCounts
	if err := o.svc.UpdateSession(ctx, sess); err != nil {
		return o.failSession(ctx, sess, rec, err)
	}
	if err := o.svc.Transition(ctx, sess, StatusCompleted); err != nil {
		return o.failSession(ctx, sess, rec, err)
	}
	if err := o.succeed(ctx, rec, sess.SourceRowCounts, tgtCounts); err != nil {
		return o.fail(ctx, rec, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// internal helpers
// ---------------------------------------------------------------------------

// validateSource confirms the source is reachable and the client tools work by
// asking for its server version. A failure here is the common "source
// unreachable / wrong password" off-ramp and is surfaced (scrubbed) to the user.
func (o *Orchestrator) validateSource(ctx context.Context, src ConnInfo) error {
	if _, err := o.engine.Version(ctx, src); err != nil {
		return core.ExecError("cannot reach source %s", src.Redacted()).
			WithHint("check the host, port, credentials, and that the source allows this connection").
			Wrap(err)
	}
	return nil
}

// validateTargetOverwrite enforces the overwrite-safety gate: if the target
// database already holds user tables and the job did not opt into overwrite,
// refuse with a typed-name SafetyError so the UI can prompt for confirmation.
func (o *Orchestrator) validateTargetOverwrite(ctx context.Context, job Job) error {
	if job.Overwrite {
		return nil
	}
	nonEmpty, err := o.engine.DatabaseNonEmpty(ctx, job.Target, job.TargetDatabase)
	if err != nil {
		return err
	}
	if nonEmpty {
		return core.RequireConfirmation(
			"overwrite database "+job.TargetDatabase, job.TargetDatabase, "")
	}
	return nil
}

// prepareTarget makes the target database ready to receive a restore. When
// overwriting it drops and recreates the database for a clean slate; otherwise
// it creates the database if it does not yet exist.
func (o *Orchestrator) prepareTarget(ctx context.Context, job Job) error {
	if job.Overwrite {
		if err := o.engine.DropDatabase(ctx, job.Target, job.TargetDatabase); err != nil {
			return err
		}
		return o.engine.CreateDatabase(ctx, job.Target, job.TargetDatabase, "")
	}
	exists, err := o.engine.DatabaseExists(ctx, job.Target, job.TargetDatabase)
	if err != nil {
		return err
	}
	if !exists {
		return o.engine.CreateDatabase(ctx, job.Target, job.TargetDatabase, "")
	}
	return nil
}

// verify pulls row counts from both sides and fails on any mismatch. It returns
// the two count maps so the caller can record them on success.
func (o *Orchestrator) verify(ctx context.Context, src ConnInfo, srcDB string, tgt ConnInfo, tgtDB string) (map[string]int64, map[string]int64, error) {
	srcCounts, err := o.engine.RowCounts(ctx, src, srcDB)
	if err != nil {
		return nil, nil, err
	}
	tgtCounts, err := o.engine.RowCounts(ctx, tgt, tgtDB)
	if err != nil {
		return nil, nil, err
	}
	if diffs := CompareRowCounts(srcCounts, tgtCounts); len(diffs) > 0 {
		return nil, nil, rowMismatchError(diffs)
	}
	return srcCounts, tgtCounts, nil
}

// stage records a status+phase boundary, logging (not failing) a Recorder error.
func (o *Orchestrator) stage(ctx context.Context, rec Recorder, status Status, phase Phase) {
	if err := rec.Stage(ctx, status, phase); err != nil {
		o.log.Warn("migration recorder Stage failed", "phase", string(phase), "error", err)
	}
}

// fail records the (scrubbed) failure and returns it unchanged so callers can
// `return o.fail(...)`. A Recorder error is logged, never masking the cause.
func (o *Orchestrator) fail(ctx context.Context, rec Recorder, cause error) error {
	if rerr := rec.Fail(ctx, cause); rerr != nil {
		o.log.Warn("migration recorder Fail failed", "error", rerr)
	}
	return cause
}

// failSession marks the cross-panel S3 session failed (best-effort) AND records
// the local failure. It is used by the ssh-less export/import paths for any
// off-ramp that occurs after the session has already advanced past StatusWaiting
// (i.e. it is in exporting/importing): without this the session document would
// rot in a non-terminal state on S3 and the other panel — which only ever
// observes the session — would never see the failure or be able to retry.
//
// The session's Error is set to the already-redacted cause and a transition to
// StatusFailed is attempted only when the state machine permits it; transition
// errors are logged, never masking the original cause.
func (o *Orchestrator) failSession(ctx context.Context, sess *MigrationSession, rec Recorder, cause error) error {
	if sess != nil && o.svc != nil && CanTransition(sess.Status, StatusFailed) {
		sess.Error = cause.Error()
		if terr := o.svc.Transition(ctx, sess, StatusFailed); terr != nil {
			o.log.Warn("could not transition session to failed", "code", sess.Code, "error", terr)
		}
	}
	return o.fail(ctx, rec, cause)
}

// succeed records completion with verified counts and returns any Recorder error.
func (o *Orchestrator) succeed(ctx context.Context, rec Recorder, src, tgt map[string]int64) error {
	return rec.Succeed(ctx, src, tgt)
}

// cleanup removes the per-job temp directory best-effort. An empty workDir is a
// no-op (tests that stub the engine never write a file).
func (o *Orchestrator) cleanup() {
	if o.workDir == "" {
		return
	}
	if err := os.RemoveAll(o.workDir); err != nil {
		o.log.Warn("migration workdir cleanup failed", "dir", o.workDir, "error", err)
	}
}

// rowMismatchError builds a verification failure carrying a compact, redaction-
// safe summary of the mismatched tables.
func rowMismatchError(diffs []RowCountDiff) error {
	var b strings.Builder
	for i, d := range diffs {
		if i >= 10 {
			fmt.Fprintf(&b, ", and %d more", len(diffs)-i)
			break
		}
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%s(src=%d,tgt=%d)", d.Table, d.Source, d.Target)
	}
	return core.InternalError("row count verification failed: %d table(s) differ", len(diffs)).
		WithDetail("mismatches", b.String()).
		WithHint("the restore did not reproduce the source row counts")
}

// mergeCounts folds a per-database count map into an aggregate map, prefixing
// each table key with the database name so keys stay unique across databases.
func mergeCounts(dst map[string]int64, db string, counts map[string]int64) {
	for table, n := range counts {
		dst[db+"."+table] = n
	}
}

// sha256Hex returns the hex-encoded SHA-256 of a byte slice.
func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
