package server

import (
	"context"
	"time"

	"github.com/venkatesh-sekar/indiepg/internal/backup"
	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/migrate"
)

// Compile-time assertion that the real S3 adapter satisfies the drop-off
// transport surface, so dropTransportFor can hand it to ImportFromDrop.
var _ migrate.DropTransport = (*backup.S3ObjectStore)(nil)

// runDropImportWorker is the background worker for a drop-off import: it streams
// the dump the source pushed to S3 into the local target, verifies it, and
// records the outcome. It runs on a BOUNDED background context (workerContext) so
// it survives the HTTP response but a stalled transfer cannot wedge it forever.
// The Orchestrator records migration progress via the recorder; this wrapper also
// updates the dropoff_sessions status terminally.
func (s *Server) runDropImportWorker(id int64, code, targetDB string) {
	ctx, cancel := workerContext()
	defer cancel()
	// Release the process-local target claim handleStartDropoff acquired, so the next
	// import into this local target is admitted once this worker exits.
	defer s.releaseImportTarget(targetDB)
	rec := newStoreRecorder(s.store, id)

	// Capture the transport ONCE for the whole import: a config save cannot swap it
	// mid-run anyway (handleUpdateConfig refuses to change the S3 target while a
	// non-terminal drop-off session like this one exists), and reading it once keeps
	// the dump's read/cleanup bound to the bucket it was uploaded to.
	drops := s.dropTransport()
	if drops == nil {
		err := errDropRequiresS3()
		_ = rec.Fail(ctx, err)
		s.finishDropoff(ctx, code, err)
		return
	}

	tgt, err := s.localTargetConn(ctx)
	if err != nil {
		ferr := core.InternalError("cannot reach local Postgres to restore into").Wrap(err)
		_ = rec.Fail(ctx, ferr)
		s.finishDropoff(ctx, code, ferr)
		return
	}

	drec, err := s.store.GetDropoffByCode(ctx, code)
	if err != nil {
		_ = rec.Fail(ctx, err)
		// Match the other early-exit branches: move the dropoff record to a terminal
		// state too, so a failed reload doesn't leave the session wedged 'importing'
		// (with a failed underlying migration) until a restart/expiry sweep.
		s.finishDropoff(ctx, code, err)
		return
	}

	workDir, err := jobWorkDir(id)
	if err != nil {
		_ = rec.Fail(ctx, err)
		s.finishDropoff(ctx, code, err)
		return
	}

	spec := migrate.DropImportSpec{
		Code:           code,
		DumpKey:        drec.DumpKey,
		MetaKey:        drec.MetaKey,
		TargetDatabase: drec.TargetDatabase,
		Overwrite:      drec.Overwrite,
		Target:         tgt,
	}
	// svc/os are nil: ImportFromDrop takes the DropTransport as an argument and
	// never touches the Orchestrator's ssh-less session plumbing.
	orch := migrate.NewOrchestrator(s.migrateEngine, nil, nil, workDir, s.log)
	ierr := orch.ImportFromDrop(ctx, drops, spec, rec)
	if ierr != nil {
		s.log.Warn("drop-off import failed", "id", id, "code", code, "err", ierr)
	}
	s.finishDropoff(ctx, code, ierr)
}

// finishDropoff records the terminal dropoff_sessions status. Like the recorder's
// Fail/Succeed, the write runs on a context detached from the worker's
// cancellation/deadline (then re-bounded), so a timeout-expired worker context —
// the headline stalled-transfer scenario — still persists the outcome instead of
// leaving the session stuck "importing".
//
// The write is CONDITIONAL on the session still being 'importing'
// (FinalizeDropoffFromImporting): if a concurrent cancel — or the expiry sweep —
// moved it to a terminal state while this worker was mid-restore, that decision is
// authoritative and must not be clobbered. Without this guard a cancelled import
// whose restore happened to finish would resurrect itself to 'completed', silently
// reporting success for an import the operator had cancelled.
func (s *Server) finishDropoff(ctx context.Context, code string, jobErr error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()

	status := string(migrate.DropCompleted)
	errMsg := ""
	if jobErr != nil {
		status = string(migrate.DropFailed)
		errMsg = jobErr.Error()
	}
	won, err := s.store.FinalizeDropoffFromImporting(ctx, code, status, errMsg)
	if err != nil {
		s.log.Warn("could not finalize drop-off status", "code", code, "err", err)
		return
	}
	if !won {
		s.log.Info("drop-off already finalized by cancel or sweep; worker outcome not applied",
			"code", code, "worker_status", status)
	}
}

// sweepExpiredDropoffs deletes the S3 objects of past-TTL drop-off sessions (a
// full database at rest must not linger past its TTL) and moves them to a terminal
// 'expired' state. ListExpiredDropoffs excludes actively-'importing' sessions
// (never reclaim a live import's dump) and includes 'failed' ones (whose
// kept-for-retry dump must still be reclaimed) AND 'completed' ones (a backstop for
// a success-path delete that failed transiently, which would otherwise orphan the
// dump forever). Deletes are idempotent, so a session whose objects are already
// gone is a cheap no-op. Best-effort: per-session errors only log. Called on
// startup and on a periodic schedule.
func (s *Server) sweepExpiredDropoffs(ctx context.Context) error {
	drops := s.dropTransport()
	expired, err := s.store.ListExpiredDropoffs(ctx, time.Now().UTC(), 100)
	if err != nil {
		return err
	}
	for _, rec := range expired {
		// Re-read immediately before reclaiming: the list ran a moment ago and a
		// start could have raced in and claimed this session to 'importing' since
		// (ClaimDropoffForImport flips atomically from waiting/uploaded/failed).
		// Deleting a just-started import's dump out from under its worker would fail
		// the job spuriously, so skip a row that is no longer reclaimable. (A tiny
		// window between this re-read and the delete remains; its worst case is a
		// recoverable spurious failure, not data loss — the worker's first act is a
		// StatObject that surfaces the missing object as a retryable error.)
		cur, gerr := s.store.GetDropoffByCode(ctx, rec.Code)
		if gerr != nil {
			s.log.Warn("could not re-read drop-off before reclaim", "code", rec.Code, "err", gerr)
			continue
		}
		switch migrate.DropStatus(cur.Status) {
		case migrate.DropImporting, migrate.DropExpired:
			continue
		}
		// A persisted 'expired' is the panel's promise that the full database at rest
		// was reclaimed (the sweep skips 'expired' rows forever after). So only make
		// that transition once BOTH idempotent deletes actually succeed. With no
		// transport configured, or on ANY delete failure (transient S3 error, rotated
		// creds), leave the row non-terminal so the NEXT sweep retries it — marking it
		// 'expired' anyway would orphan the dump permanently.
		if drops == nil {
			s.log.Warn("cannot reclaim expired drop-off objects: no S3 transport configured; will retry", "code", cur.Code)
			continue
		}
		dumpErr := drops.DeleteObject(ctx, cur.DumpKey)
		if dumpErr != nil {
			s.log.Warn("could not delete expired drop-off dump; leaving for the next sweep", "code", cur.Code, "err", dumpErr)
		}
		metaErr := drops.DeleteObject(ctx, cur.MetaKey)
		if metaErr != nil {
			s.log.Warn("could not delete expired drop-off metadata; leaving for the next sweep", "code", cur.Code, "err", metaErr)
		}
		if dumpErr != nil || metaErr != nil {
			continue // not fully reclaimed: do NOT mark expired, retry next sweep
		}
		// Both objects are gone (or were already absent — deletes are idempotent).
		// Move to the terminal 'expired' state so the row drains out of the sweep's
		// set (it won't be reclaimed again every cycle) while preserving any failure
		// Error, so the UI can still explain why a previously-failed drop-off ended.
		cur.Status = string(migrate.DropExpired)
		if uerr := s.store.UpdateDropoff(ctx, *cur); uerr != nil {
			s.log.Warn("could not mark drop-off expired", "code", cur.Code, "err", uerr)
		}
	}
	return nil
}
