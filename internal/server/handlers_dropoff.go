package server

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/migrate"
	"github.com/venkatesh-sekar/indiepg/internal/store"
)

// migratePushScriptURL is the hosted push script the operator pipes into sh on
// the source box, mirroring scripts/install.sh's one-liner convention.
const migratePushScriptURL = "https://raw.githubusercontent.com/venkatesh-sekar/indiepg/main/scripts/migrate-push.sh"

// dropoffPrecheckTimeout bounds the best-effort, mint-time "is the target already
// non-empty?" probe against the local Postgres, so a slow or unreachable cluster
// can never stall a drop-off mint (the import-time gate is the real authority).
const dropoffPrecheckTimeout = 10 * time.Second

// createDropoffRequest mints a drop-off session for a single target database. A
// destructive overwrite of a non-empty target requires the typed-name Confirm
// (re-typing TargetDatabase), exactly like the direct single-db handler.
type createDropoffRequest struct {
	TargetDatabase string `json:"target_database"`
	Overwrite      bool   `json:"overwrite"`
	Confirm        string `json:"confirm"`
}

// startDropoffRequest is the OPTIONAL body of POST /migrate/drops/{code}/start. A
// session minted with overwrite=true requires Confirm to re-echo the target
// database name (the DROP runs at Start, not at mint), mirroring the single-db
// handler; a non-overwrite Start may omit the body entirely.
type startDropoffRequest struct {
	Confirm string `json:"confirm"`
}

// createDropoffResponse is returned ONCE at mint. It is the only place the
// presigned-URL-bearing commands are served — they are never re-served by the
// status endpoint.
type createDropoffResponse struct {
	Code           string    `json:"code"`
	TargetDatabase string    `json:"target_database"`
	Overwrite      bool      `json:"overwrite"`
	ExpiresAt      time.Time `json:"expires_at"`
	CommandDocker  string    `json:"command_docker"`
	CommandNative  string    `json:"command_native"`
}

// dropoffStatusResponse is the safe, re-servable view of a drop-off session: no
// URLs, no command. The SPA polls it for the upload-readiness badge and, once the
// import starts, switches to polling GET /migrate/{migration_id}.
type dropoffStatusResponse struct {
	Code           string    `json:"code"`
	Status         string    `json:"status"`
	TargetDatabase string    `json:"target_database"`
	Overwrite      bool      `json:"overwrite"`
	ExpiresAt      time.Time `json:"expires_at"`
	MigrationID    *int64    `json:"migration_id,omitempty"`
	ByteSize       int64     `json:"byte_size"`
	Error          string    `json:"error,omitempty"`
}

// errDropRequiresS3 is the honest "S3 required" error for the drop-off endpoints
// when no S3 target is configured (s.drops == nil). The message contains "S3" so
// the SPA's /S3/i callout fires, mirroring errSSHLessRequiresS3.
func errDropRequiresS3() error {
	return core.InternalError("drop-off link migration requires S3 object storage").
		WithHint("configure an S3 backup target in Settings, or use Direct pull which needs no S3")
}

// s3PutProber is the optional fast-reachability probe a drop transport may
// implement. The real *backup.S3ObjectStore does (tolerating a PutObject-only
// policy); the in-memory test fakes don't and fall back to a plain StatObject.
type s3PutProber interface {
	ProbePutReachable(ctx context.Context, key string) error
}

// probeDropTransport fails fast when S3 is misconfigured BEFORE a mint hands out a
// presigned-PUT command. It prefers the transport's PUT-reachability probe (which
// tolerates a ListBucket/GetObject-less, PutObject-only policy — the only
// permission the source actually exercises) and falls back to a plain HEAD via
// StatObject for transports that don't implement it (the test fakes).
func (s *Server) probeDropTransport(ctx context.Context, drops migrate.DropTransport, key string) error {
	if p, ok := drops.(s3PutProber); ok {
		return p.ProbePutReachable(ctx, key)
	}
	_, _, err := drops.StatObject(ctx, key)
	return err
}

// handleListDropoffs returns the active (non-terminal, not-yet-expired) drop-off
// sessions as the safe status view — no URLs, no command. It is the recovery path
// for a minted-but-not-started session whose code was lost to a browser reload/tab
// close during the mint -> push -> Start window: the operator returns to a list and
// can resume Start/Cancel instead of waiting out the expiry sweep. Requires S3.
// GET /migrate/drops.
func (s *Server) handleListDropoffs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	drops := s.dropTransport()
	if drops == nil {
		writeError(w, errDropRequiresS3())
		return
	}
	recs, err := s.store.ListActiveDropoffs(ctx, nowUTC(), 50)
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]dropoffStatusResponse, 0, len(recs))
	for i := range recs {
		// Apply the same upload-readiness flip (waiting -> uploaded) the single-code
		// status endpoint does, so the list badge says "ready to import" the moment
		// the source's meta.json lands and the operator can Start straight from here.
		rec := s.refreshDropoff(ctx, drops, &recs[i])
		out = append(out, toDropoffStatusResponse(*rec))
	}
	writeData(w, http.StatusOK, out)
}

// handleCreateDropoff mints a drop-off session: two presigned S3 PUT URLs (dump +
// meta.json), a local dropoff_sessions record (KEYS only), and a paste-able push
// command. Requires S3. POST /migrate/drops.
func (s *Server) handleCreateDropoff(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	drops := s.dropTransport()
	if drops == nil {
		writeError(w, errDropRequiresS3())
		return
	}

	var req createDropoffRequest
	if err := decodeJSON(r, &req, maxBodyBytes); err != nil {
		writeError(w, err)
		return
	}
	if err := core.ValidateIdentifier(req.TargetDatabase, "database"); err != nil {
		s.audit(ctx, "migrate_dropoff_create", req.TargetDatabase, "failure", "invalid target database", core.CodeOf(err))
		writeError(w, err)
		return
	}
	// A destructive overwrite must be authorized by re-typing the target database
	// name server-side; a bare boolean is never sufficient (mirrors single-db).
	if req.Overwrite {
		if err := core.RequireConfirmation("overwrite database "+req.TargetDatabase, req.TargetDatabase, req.Confirm); err != nil {
			s.audit(ctx, "migrate_dropoff_create", req.TargetDatabase, "failure", "overwrite not confirmed", core.CodeOf(err))
			writeError(w, err)
			return
		}
	}

	// Mint-time overwrite pre-check (best-effort): if the local target already holds
	// tables and overwrite was NOT authorized, refuse NOW — while the decision is
	// free to redo — instead of after the source has run an expensive, hard-to-repeat
	// pg_dump + upload only to hit the SAME gate at import time, where the uploaded
	// dump cannot be re-driven from the unreachable source. The import-time gate
	// (Orchestrator.validateTargetOverwrite) remains the authority; this only
	// front-loads the common case. A local Postgres unreachable here is NOT fatal to
	// minting (the import can't run yet either) — log and defer to the import gate.
	if !req.Overwrite {
		// Bound the probe so a slow/unreachable local Postgres can never stall the
		// mint: on any error (timeout included) we log and defer to the import gate.
		checkCtx, cancelCheck := context.WithTimeout(ctx, dropoffPrecheckTimeout)
		defer cancelCheck()
		if tgt, terr := s.localTargetConn(checkCtx); terr != nil {
			s.log.Warn("drop-off mint: could not reach local Postgres for the overwrite pre-check; deferring to the import-time gate", "err", terr)
		} else if nonEmpty, nerr := s.migrateEngine.DatabaseNonEmpty(checkCtx, tgt, req.TargetDatabase); nerr != nil {
			s.log.Warn("drop-off mint: could not check whether the target is empty; deferring to the import-time gate", "database", req.TargetDatabase, "err", nerr)
		} else if nonEmpty {
			// req.Confirm is empty in the non-overwrite path, so this yields a CodeSafety
			// RequireConfirmation the SPA turns into "tick Replace and type the name".
			se := core.RequireConfirmation("overwrite database "+req.TargetDatabase, req.TargetDatabase, req.Confirm)
			se.Err.Hint = "a database named " + req.TargetDatabase + " already exists and is not empty here — enable Replace and re-type its name to overwrite it on import"
			s.audit(ctx, "migrate_dropoff_create", req.TargetDatabase, "failure", "target non-empty; overwrite not confirmed", core.CodeOf(se))
			writeError(w, se)
			return
		}
	}

	code := migrate.GenerateCode()
	dumpKey := migrate.DropDumpKey(code)
	metaKey := migrate.DropMetaKey(code)
	probeKey := migrate.DropProbeKey(code)

	// ONE absolute deadline for the whole session: the persisted expiry, the expiry
	// sweep's cutoff, AND both presigned-URL validities all derive from it. Each URL
	// is later signed for only its REMAINING time to this deadline, so neither can
	// outlive the session/sweep and recreate an object after cleanup. (Signing a
	// fresh full TTL per URL — computed after the probe and the first signing —
	// would let each URL expire slightly AFTER the persisted expiry: the bug.)
	deadline := nowUTC().Add(migrate.DropDefaultTTL)
	expiresAt := deadline

	// Probe S3 reachability with a cheap authenticated request BEFORE handing out a
	// command. PresignPut is a purely local SigV4 signing op and NewS3ObjectStore
	// only checks that endpoint/bucket are non-empty, so without this a mint would
	// happily return a paste-able command even with wrong access/secret keys or an
	// unreachable bucket — and the failure would only surface on the hard-to-reach
	// source as migrate-push.sh's misleading "the link may have expired". The probe
	// exercises the FULL object lifecycle the panel itself needs (PUT a disposable
	// probe object, then stat, read and delete it — see S3ObjectStore.ProbePutReachable):
	// a PutObject-only policy that lets the source upload but denies the panel's
	// later stat/get/delete must fail the mint NOW, since it would otherwise import-
	// fail or orphan the dump with no way to clean up.
	if perr := s.probeDropTransport(ctx, drops, probeKey); perr != nil {
		s.audit(ctx, "migrate_dropoff_create", req.TargetDatabase, "failure", "S3 reachability probe failed", core.CodeOf(perr))
		writeError(w, core.InternalError("S3 object storage is not reachable with the configured credentials").
			WithHint("check the S3 endpoint, bucket, and access/secret keys in Settings, then try again").
			Wrap(perr))
		return
	}

	// Sign each URL for only the time LEFT to the shared deadline, and abort if the
	// probe (or anything before this) already burned through it — never hand out a
	// URL that outlives the session.
	dumpTTL := deadline.Sub(nowUTC())
	if dumpTTL <= 0 {
		writeError(w, core.InternalError("drop-off link expired before it could be issued; please try again"))
		return
	}
	dumpURL, err := drops.PresignPut(ctx, dumpKey, dumpTTL)
	if err != nil {
		s.audit(ctx, "migrate_dropoff_create", req.TargetDatabase, "failure", "presign dump url failed", core.CodeOf(err))
		writeError(w, err)
		return
	}
	metaTTL := deadline.Sub(nowUTC())
	if metaTTL <= 0 {
		writeError(w, core.InternalError("drop-off link expired before it could be issued; please try again"))
		return
	}
	metaURL, err := drops.PresignPut(ctx, metaKey, metaTTL)
	if err != nil {
		s.audit(ctx, "migrate_dropoff_create", req.TargetDatabase, "failure", "presign meta url failed", core.CodeOf(err))
		writeError(w, err)
		return
	}

	if _, err := s.store.InsertDropoff(ctx, dropoffStoreRecord(code, dumpKey, metaKey, req.TargetDatabase, req.Overwrite, expiresAt)); err != nil {
		s.audit(ctx, "migrate_dropoff_create", req.TargetDatabase, "failure", "could not record drop-off", core.CodeOf(err))
		writeError(w, err)
		return
	}

	s.audit(ctx, "migrate_dropoff_create", code, "success",
		fmt.Sprintf("drop-off session for database %s (overwrite=%t)", req.TargetDatabase, req.Overwrite), "")
	writeData(w, http.StatusCreated, createDropoffResponse{
		Code:           code,
		TargetDatabase: req.TargetDatabase,
		Overwrite:      req.Overwrite,
		ExpiresAt:      expiresAt,
		CommandDocker:  dropoffCommandDocker(dumpURL, metaURL),
		CommandNative:  dropoffCommandNative(dumpURL, metaURL),
	})
}

// handleGetDropoff returns the safe status view, flipping waiting_for_upload ->
// uploaded once meta.json is present and applying expiry-on-read. It NEVER
// re-serves the command or URLs. Requires S3. GET /migrate/drops/{code}.
func (s *Server) handleGetDropoff(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	drops := s.dropTransport()
	if drops == nil {
		writeError(w, errDropRequiresS3())
		return
	}
	code := chi.URLParam(r, "code")
	rec, err := s.store.GetDropoffByCode(ctx, code)
	if err != nil {
		writeError(w, err)
		return
	}

	rec = s.refreshDropoff(ctx, drops, rec)
	writeData(w, http.StatusOK, toDropoffStatusResponse(*rec))
}

// handleStartDropoff begins the import once meta.json is present: it inserts the
// migrations row, links it, and spawns the import worker. The SPA then polls the
// existing GET /migrate/{id} path. Requires S3. POST /migrate/drops/{code}/start.
func (s *Server) handleStartDropoff(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	drops := s.dropTransport()
	if drops == nil {
		writeError(w, errDropRequiresS3())
		return
	}
	code := chi.URLParam(r, "code")
	rec, err := s.store.GetDropoffByCode(ctx, code)
	if err != nil {
		writeError(w, err)
		return
	}

	// The import is where the DROP DATABASE actually runs (the mint only recorded
	// intent), so a destructive overwrite must be RE-AUTHORIZED here with the typed
	// target name — never a bare flag. The body is optional (a non-overwrite Start
	// needs none) but when the session was minted with overwrite=true the operator
	// must echo the database name in `confirm`, or the import is refused with a typed
	// CodeSafety error. Without this gate a direct API call could bypass the SPA's
	// confirm dialog and drop the database. Mirrors the single-db handler.
	var startReq startDropoffRequest
	if err := decodeJSONOptional(r, &startReq, maxBodyBytes); err != nil {
		writeError(w, err)
		return
	}
	if rec.Overwrite {
		if err := core.RequireConfirmation("overwrite database "+rec.TargetDatabase, rec.TargetDatabase, startReq.Confirm); err != nil {
			s.audit(ctx, "migrate_dropoff_start", code, "failure", "overwrite not confirmed", core.CodeOf(err))
			writeError(w, err)
			return
		}
	}

	switch migrate.DropStatus(rec.Status) {
	case migrate.DropImporting:
		writeError(w, core.ConflictError("drop-off %s is already importing", code))
		return
	case migrate.DropCompleted:
		writeError(w, core.ConflictError("drop-off %s already completed", code))
		return
	case migrate.DropCanceled:
		writeError(w, core.ConflictError("drop-off %s was cancelled and cannot be started", code).
			WithHint("create a new drop-off link"))
		return
	}
	if rec.ExpiresAt.Before(nowUTC()) {
		writeError(w, core.ConflictError("drop-off %s has expired", code).
			WithHint("create a new drop-off link"))
		return
	}

	// meta.json present == upload complete & verifiable. Refuse otherwise.
	if _, metaExists, serr := drops.StatObject(ctx, rec.MetaKey); serr != nil {
		writeError(w, serr)
		return
	} else if !metaExists {
		writeError(w, core.ConflictError("drop-off %s is not uploaded yet", code).
			WithHint("run the push command on the source, then click Start"))
		return
	}
	dumpSize, dumpExists, serr := drops.StatObject(ctx, rec.DumpKey)
	if serr != nil {
		writeError(w, serr)
		return
	}
	if !dumpExists {
		writeError(w, core.ConflictError("drop-off %s dump object is missing", code).
			WithHint("the upload did not complete; re-run the push command"))
		return
	}
	if dumpSize > migrate.MaxDropBytes {
		writeError(w, core.ValidationError(
			"drop-off %s dump is %d MiB, over the %d MiB single-PUT limit — use the direct-pull migration instead",
			code, dumpSize>>20, migrate.MaxDropBytes>>20).
			WithHint("direct-pull streams the dump and has no size limit"))
		return
	}

	// Atomically claim the start. Two concurrent POSTs (a double-click, two tabs,
	// a retried request, direct API use) both pass the status/stat checks above,
	// but only ONE wins this conditional flip to 'importing' — without it both
	// would InsertMigration and spawn a worker, racing two pg_restores into the
	// same (destructive) target. The loser is told the session already moved on.
	won, err := s.store.ClaimDropoffForImport(ctx, code)
	if err != nil {
		s.audit(ctx, "migrate_dropoff_start", code, "failure", "could not claim drop-off", core.CodeOf(err))
		writeError(w, err)
		return
	}
	if !won {
		writeError(w, core.ConflictError("drop-off %s is already importing or finished", code).
			WithHint("refresh to see its current status"))
		return
	}

	mrec := dropoffMigrationRecord(code, rec.TargetDatabase, rec.Overwrite)
	id, err := s.store.InsertMigration(ctx, mrec)
	if err != nil {
		// Roll back the claim so the session isn't wedged 'importing' with no
		// worker behind it: finishDropoff marks it failed (retryable) with the error.
		s.finishDropoff(ctx, code, err)
		s.audit(ctx, "migrate_dropoff_start", code, "failure", "could not record migration", core.CodeOf(err))
		writeError(w, err)
		return
	}

	rec.MigrationID = &id
	rec.Status = string(migrate.DropImporting)
	rec.ByteSize = dumpSize
	rec.Error = ""
	if err := s.store.UpdateDropoff(ctx, *rec); err != nil {
		// The migrations row was already inserted (status 'importing') but the worker
		// is only spawned below, AFTER this link succeeds. Mark that row failed before
		// returning so a failed link does not leave a phantom in-flight job lingering
		// in History until the next restart's SweepRunningMigrations reclaims it; then
		// finalize the dropoff session itself.
		s.failMigrationRow(ctx, id, err)
		s.finishDropoff(ctx, code, err)
		s.audit(ctx, "migrate_dropoff_start", code, "failure", "could not link migration", core.CodeOf(err))
		writeError(w, err)
		return
	}

	go s.runDropImportWorker(id, code)

	s.audit(ctx, "migrate_dropoff_start", code, "success",
		migrationSummaryFor(migrate.ModeDropOff, "drop-off "+code), "")
	writeData(w, http.StatusAccepted, migrateStartedResponse{ID: id, Status: string(migrate.StatusImporting)})
}

// handleCancelDropoff deletes the dump+meta objects (idempotent) and marks the
// session (and any linked migration) failed/cancelled. Requires S3.
// DELETE /migrate/drops/{code}.
func (s *Server) handleCancelDropoff(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	drops := s.dropTransport()
	if drops == nil {
		writeError(w, errDropRequiresS3())
		return
	}
	code := chi.URLParam(r, "code")
	rec, err := s.store.GetDropoffByCode(ctx, code)
	if err != nil {
		writeError(w, err)
		return
	}

	// REFUSE to cancel a running import. Cancelling mid-import could interrupt an
	// overwrite AFTER the original database was already dropped, and then this
	// handler would delete the S3 recovery dump — destroying the only copy — while
	// reporting success. An import either completes or fails on its own; the failed
	// path keeps the dump in S3 for a retry, and only THEN can it be cancelled to
	// clean up. (The store-side MarkDropoffCancelled also excludes 'importing'
	// atomically, closing the read-then-write race below.)
	if migrate.DropStatus(rec.Status) == migrate.DropImporting {
		writeError(w, core.ConflictError("drop-off %s is importing and cannot be cancelled now", code).
			WithHint("wait for the import to finish or fail; a failed import can then be cancelled to clean up"))
		return
	}

	// Atomically claim the cancel: 'canceled' (terminal, NOT the retryable 'failed')
	// only when the row is not already completed/expired/importing. A Start that
	// raced the row into 'importing' between the read above and here makes this
	// no-op (won=false), so we must NOT then delete the objects out from under it.
	won, uerr := s.store.MarkDropoffCancelled(ctx, code)
	if uerr != nil {
		s.log.Warn("could not mark cancelled drop-off", "code", code, "err", uerr)
		writeError(w, uerr)
		return
	}
	if !won {
		writeError(w, core.ConflictError("drop-off %s can no longer be cancelled", code).
			WithHint("it just started importing or already finished — refresh to see its status"))
		return
	}

	// The cancel is committed and the session is no longer startable, so it is now
	// safe to reclaim the data at rest. Best-effort + idempotent: a missing object
	// is fine, and the expiry sweep is the backstop for a transient delete failure.
	if derr := drops.DeleteObject(ctx, rec.DumpKey); derr != nil {
		s.log.Warn("could not delete drop-off dump on cancel; the expiry sweep will reclaim it", "code", code, "err", derr)
	}
	if derr := drops.DeleteObject(ctx, rec.MetaKey); derr != nil {
		s.log.Warn("could not delete drop-off metadata on cancel; the expiry sweep will reclaim it", "code", code, "err", derr)
	}

	s.audit(ctx, "migrate_dropoff_cancel", code, "success", "drop-off session cancelled", "")
	writeData(w, http.StatusOK, map[string]any{"ok": true})
}

// refreshDropoff applies expiry-on-read and the upload-readiness flip
// (waiting_for_upload -> uploaded) and persists any change, returning the updated
// record. Best-effort: a stat/store error leaves the record as read.
func (s *Server) refreshDropoff(ctx context.Context, drops migrate.DropTransport, rec *store.DropoffRecord) *store.DropoffRecord {
	switch migrate.DropStatus(rec.Status) {
	case migrate.DropCompleted, migrate.DropFailed, migrate.DropCanceled, migrate.DropExpired, migrate.DropImporting:
		return rec // terminal or actively importing: nothing to flip
	}
	if rec.ExpiresAt.Before(nowUTC()) {
		// Report expired WITHOUT persisting it. The expiry sweep is the single
		// authority that persists 'expired' AND deletes the dump+metadata in the
		// same step, so a persisted 'expired' always implies the full database at
		// rest was reclaimed. Marking it 'expired' here (the read path can't delete
		// reliably) would orphan the dump forever, since the sweep skips rows that
		// are already 'expired'. Leaving the row non-terminal keeps it in the
		// sweep's set; the response still shows 'expired' to the operator.
		rec.Status = string(migrate.DropExpired)
		return rec
	}
	if rec.Status != string(migrate.DropWaiting) {
		return rec
	}
	// meta.json is uploaded LAST by the push script, so its presence means the dump
	// upload completed and the session is ready to import.
	size, exists, err := drops.StatObject(ctx, rec.MetaKey)
	if err != nil {
		s.log.Warn("could not stat drop-off metadata on read", "code", rec.Code, "err", err)
		return rec
	}
	if !exists {
		return rec
	}
	_ = size // meta size itself is not surfaced; the dump size is.
	rec.Status = string(migrate.DropUploaded)
	if dumpSize, dumpExists, derr := drops.StatObject(ctx, rec.DumpKey); derr == nil && dumpExists {
		rec.ByteSize = dumpSize
	}
	if uerr := s.store.UpdateDropoff(ctx, *rec); uerr != nil {
		s.log.Warn("could not flip drop-off to uploaded on read", "code", rec.Code, "err", uerr)
	}
	return rec
}

// toDropoffStatusResponse maps a store record to the safe wire shape.
func toDropoffStatusResponse(d store.DropoffRecord) dropoffStatusResponse {
	return dropoffStatusResponse{
		Code:           d.Code,
		Status:         d.Status,
		TargetDatabase: d.TargetDatabase,
		Overwrite:      d.Overwrite,
		ExpiresAt:      d.ExpiresAt,
		MigrationID:    d.MigrationID,
		ByteSize:       d.ByteSize,
		Error:          d.Error,
	}
}

// dropoffStoreRecord builds the initial dropoff_sessions record (keys only,
// waiting for the source to upload).
func dropoffStoreRecord(code, dumpKey, metaKey, targetDB string, overwrite bool, expiresAt time.Time) store.DropoffRecord {
	return store.DropoffRecord{
		Code:           code,
		DumpKey:        dumpKey,
		MetaKey:        metaKey,
		TargetDatabase: targetDB,
		Overwrite:      overwrite,
		Status:         string(migrate.DropWaiting),
		ExpiresAt:      expiresAt,
	}
}

// dropoffMigrationRecord builds the migrations-table row for a drop-off import so
// it shares the same progress/poll path as every other migration mode.
func dropoffMigrationRecord(code, targetDB string, overwrite bool) store.MigrationRecord {
	return store.MigrationRecord{
		Mode:   string(migrate.ModeDropOff),
		Role:   "target",
		Status: string(migrate.StatusImporting),
		Phase:  string(migrate.PhaseValidating),
		// The source is a box the panel cannot reach, so there is no host/user to
		// redact — label the row by its drop-off code so the shared History view's
		// "Source" column is self-describing instead of blank.
		SourceSummary:  "drop-off " + code,
		TargetDatabase: targetDB,
		Overwrite:      overwrite,
		Code:           code,
	}
}

// failMigrationRow marks an already-inserted migrations row failed. It is used on
// the drop-off start error path that aborts AFTER InsertMigration but BEFORE the
// import worker is spawned (a failed UpdateDropoff link), so the row is never left
// a phantom 'importing' job with no worker behind it until a restart sweep reclaims
// it. Best-effort: any store error only logs (the request is already failing, and a
// restart sweep is the backstop). The cause is the same store error surfaced to the
// caller and carries no source secrets.
func (s *Server) failMigrationRow(ctx context.Context, id int64, cause error) {
	mrec, err := s.store.GetMigration(ctx, id)
	if err != nil {
		s.log.Warn("could not load migration to mark failed after drop-off link failure", "id", id, "err", err)
		return
	}
	mrec.Status = string(migrate.StatusFailed)
	mrec.Phase = ""
	if cause != nil {
		mrec.Error = cause.Error()
	}
	now := nowUTC()
	mrec.FinishedAt = &now
	if uerr := s.store.UpdateMigration(ctx, *mrec); uerr != nil {
		s.log.Warn("could not mark migration failed after drop-off link failure", "id", id, "err", uerr)
	}
}

// dropoffCommandDocker / dropoffCommandNative render the paste-able push command,
// mirroring scripts/install.sh's curl|sh one-liner. The DBNAME / CONTAINER /
// SOURCE_HOST / POSTGRES_USER tokens are placeholders the operator fills in.
//
// The two presigned URLs are single-key, PUT-only, short-lived bearer tokens —
// bucket-write secrets that cannot be revoked once minted. They are passed via the
// ENVIRONMENT (INDIEPG_DUMP_URL / INDIEPG_META_URL) rather than as --dump-url /
// --meta-url argv, so they do NOT appear in the source box's process listing (`ps`
// / /proc/<pid>/cmdline is world-readable; /proc/<pid>/environ is owner-only). This
// gives the upload URLs the same anti-`ps` protection migrate-push.sh already gives
// the DB password (PGPASSWORD / /dev/tty, never a flag). migrate-push.sh reads the
// URLs from these env vars (with --dump-url/--meta-url kept as a fallback).
//
// The placeholders deliberately use plain UPPERCASE words rather than
// <angle-bracket> tokens: the common newbie mistake is pasting the line before
// substituting, and unquoted <…> would make the shell attempt input redirection
// ("No such file or directory") before sh ever reads the args. A literal DBNAME
// instead reaches migrate-push.sh, which catches it with a friendly placeholder
// guard.
func dropoffCommandDocker(dumpURL, metaURL string) string {
	return fmt.Sprintf(
		"curl -fsSL %s | INDIEPG_DUMP_URL='%s' INDIEPG_META_URL='%s' sh -s -- --db DBNAME --docker CONTAINER",
		migratePushScriptURL, dumpURL, metaURL)
}

func dropoffCommandNative(dumpURL, metaURL string) string {
	return fmt.Sprintf(
		"curl -fsSL %s | INDIEPG_DUMP_URL='%s' INDIEPG_META_URL='%s' sh -s -- --db DBNAME --host SOURCE_HOST --port 5432 --user POSTGRES_USER",
		migratePushScriptURL, dumpURL, metaURL)
}
