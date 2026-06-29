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
func (s *Server) runDropImportWorker(id int64, code string) {
	ctx, cancel := workerContext()
	defer cancel()
	// Publish this worker's cancel so handleCancelDropoff can INTERRUPT an in-flight
	// restore (the worker is detached from the request context), not merely mark the
	// row cancelled and let pg_restore run to completion. Removed on return.
	s.registerDropCancel(code, cancel)
	defer s.unregisterDropCancel(code)
	rec := newStoreRecorder(s.store, id)

	if s.drops == nil {
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
	ierr := orch.ImportFromDrop(ctx, s.drops, spec, rec)
	if ierr != nil {
		s.log.Warn("drop-off import failed", "id", id, "code", code, "err", ierr)
	}
	s.finishDropoff(ctx, code, ierr)
}

// registerDropCancel publishes an in-flight import worker's cancel func (keyed by
// session code) so handleCancelDropoff can interrupt it. unregisterDropCancel
// removes it when the worker returns.
func (s *Server) registerDropCancel(code string, cancel context.CancelFunc) {
	s.dropCancelMu.Lock()
	s.dropCancels[code] = cancel
	s.dropCancelMu.Unlock()
}

func (s *Server) unregisterDropCancel(code string) {
	s.dropCancelMu.Lock()
	delete(s.dropCancels, code)
	s.dropCancelMu.Unlock()
}

// cancelDropWorker interrupts the in-flight import worker for code by cancelling
// its context (which kills any pg_restore subprocess via exec.CommandContext). It
// is a no-op when no worker is registered (already finished, or never started).
func (s *Server) cancelDropWorker(code string) {
	s.dropCancelMu.Lock()
	cancel := s.dropCancels[code]
	s.dropCancelMu.Unlock()
	if cancel != nil {
		cancel()
	}
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
		if s.drops != nil {
			if derr := s.drops.DeleteObject(ctx, cur.DumpKey); derr != nil {
				s.log.Warn("could not delete expired drop-off dump", "code", cur.Code, "err", derr)
			}
			if derr := s.drops.DeleteObject(ctx, cur.MetaKey); derr != nil {
				s.log.Warn("could not delete expired drop-off metadata", "code", cur.Code, "err", derr)
			}
		}
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
