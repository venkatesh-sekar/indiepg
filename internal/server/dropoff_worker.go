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

// finishDropoff records the terminal dropoff_sessions status. Like the recorder's
// Fail/Succeed, the write runs on a context detached from the worker's
// cancellation/deadline (then re-bounded), so a timeout-expired worker context —
// the headline stalled-transfer scenario — still persists the outcome instead of
// leaving the session stuck "importing".
func (s *Server) finishDropoff(ctx context.Context, code string, jobErr error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()

	rec, err := s.store.GetDropoffByCode(ctx, code)
	if err != nil {
		s.log.Warn("could not load drop-off to finalize", "code", code, "err", err)
		return
	}
	if jobErr != nil {
		rec.Status = string(migrate.DropFailed)
		rec.Error = jobErr.Error()
	} else {
		rec.Status = string(migrate.DropCompleted)
		rec.Error = ""
	}
	if uerr := s.store.UpdateDropoff(ctx, *rec); uerr != nil {
		s.log.Warn("could not finalize drop-off status", "code", code, "err", uerr)
	}
}

// sweepExpiredDropoffs deletes the S3 objects of expired, non-terminal drop-off
// sessions (a full database at rest must not linger past its TTL) and marks them
// expired. Best-effort: per-session errors only log. Called on startup and on a
// periodic schedule.
func (s *Server) sweepExpiredDropoffs(ctx context.Context) error {
	expired, err := s.store.ListExpiredDropoffs(ctx, time.Now().UTC(), 100)
	if err != nil {
		return err
	}
	for _, rec := range expired {
		if s.drops != nil {
			if derr := s.drops.DeleteObject(ctx, rec.DumpKey); derr != nil {
				s.log.Warn("could not delete expired drop-off dump", "code", rec.Code, "err", derr)
			}
			if derr := s.drops.DeleteObject(ctx, rec.MetaKey); derr != nil {
				s.log.Warn("could not delete expired drop-off metadata", "code", rec.Code, "err", derr)
			}
		}
		rec.Status = string(migrate.DropExpired)
		if uerr := s.store.UpdateDropoff(ctx, rec); uerr != nil {
			s.log.Warn("could not mark drop-off expired", "code", rec.Code, "err", uerr)
		}
	}
	return nil
}
