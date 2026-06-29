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

// createDropoffRequest mints a drop-off session for a single target database. A
// destructive overwrite of a non-empty target requires the typed-name Confirm
// (re-typing TargetDatabase), exactly like the direct single-db handler.
type createDropoffRequest struct {
	TargetDatabase string `json:"target_database"`
	Overwrite      bool   `json:"overwrite"`
	Confirm        string `json:"confirm"`
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

// handleCreateDropoff mints a drop-off session: two presigned S3 PUT URLs (dump +
// meta.json), a local dropoff_sessions record (KEYS only), and a paste-able push
// command. Requires S3. POST /migrate/drops.
func (s *Server) handleCreateDropoff(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if s.drops == nil {
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

	code := migrate.GenerateCode()
	dumpKey := migrate.DropDumpKey(code)
	metaKey := migrate.DropMetaKey(code)
	ttl := migrate.DropDefaultTTL
	expiresAt := nowUTC().Add(ttl)

	dumpURL, err := s.drops.PresignPut(ctx, dumpKey, ttl)
	if err != nil {
		s.audit(ctx, "migrate_dropoff_create", req.TargetDatabase, "failure", "presign dump url failed", core.CodeOf(err))
		writeError(w, err)
		return
	}
	metaURL, err := s.drops.PresignPut(ctx, metaKey, ttl)
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
	if s.drops == nil {
		writeError(w, errDropRequiresS3())
		return
	}
	code := chi.URLParam(r, "code")
	rec, err := s.store.GetDropoffByCode(ctx, code)
	if err != nil {
		writeError(w, err)
		return
	}

	rec = s.refreshDropoff(ctx, rec)
	writeData(w, http.StatusOK, toDropoffStatusResponse(*rec))
}

// handleStartDropoff begins the import once meta.json is present: it inserts the
// migrations row, links it, and spawns the import worker. The SPA then polls the
// existing GET /migrate/{id} path. Requires S3. POST /migrate/drops/{code}/start.
func (s *Server) handleStartDropoff(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if s.drops == nil {
		writeError(w, errDropRequiresS3())
		return
	}
	code := chi.URLParam(r, "code")
	rec, err := s.store.GetDropoffByCode(ctx, code)
	if err != nil {
		writeError(w, err)
		return
	}

	switch migrate.DropStatus(rec.Status) {
	case migrate.DropImporting:
		writeError(w, core.ConflictError("drop-off %s is already importing", code))
		return
	case migrate.DropCompleted:
		writeError(w, core.ConflictError("drop-off %s already completed", code))
		return
	}
	if rec.ExpiresAt.Before(nowUTC()) {
		writeError(w, core.ConflictError("drop-off %s has expired", code).
			WithHint("create a new drop-off link"))
		return
	}

	// meta.json present == upload complete & verifiable. Refuse otherwise.
	if _, metaExists, serr := s.drops.StatObject(ctx, rec.MetaKey); serr != nil {
		writeError(w, serr)
		return
	} else if !metaExists {
		writeError(w, core.ConflictError("drop-off %s is not uploaded yet", code).
			WithHint("run the push command on the source, then click Start"))
		return
	}
	dumpSize, dumpExists, serr := s.drops.StatObject(ctx, rec.DumpKey)
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
	if s.drops == nil {
		writeError(w, errDropRequiresS3())
		return
	}
	code := chi.URLParam(r, "code")
	rec, err := s.store.GetDropoffByCode(ctx, code)
	if err != nil {
		writeError(w, err)
		return
	}

	// Best-effort delete of the data at rest (idempotent: a missing object is fine).
	if derr := s.drops.DeleteObject(ctx, rec.DumpKey); derr != nil {
		s.log.Warn("could not delete drop-off dump on cancel", "code", code, "err", derr)
	}
	if derr := s.drops.DeleteObject(ctx, rec.MetaKey); derr != nil {
		s.log.Warn("could not delete drop-off metadata on cancel", "code", code, "err", derr)
	}

	if rec.Status != string(migrate.DropCompleted) {
		rec.Status = string(migrate.DropFailed)
		rec.Error = "cancelled"
		if uerr := s.store.UpdateDropoff(ctx, *rec); uerr != nil {
			s.log.Warn("could not mark cancelled drop-off", "code", code, "err", uerr)
		}
	}
	// Mark a linked, still-running migration record cancelled too.
	if rec.MigrationID != nil {
		if mrec, merr := s.store.GetMigration(ctx, *rec.MigrationID); merr == nil && mrec.Status != string(migrate.StatusCompleted) {
			mrec.Status = string(migrate.StatusFailed)
			mrec.Phase = ""
			mrec.Error = "cancelled"
			now := nowUTC()
			mrec.FinishedAt = &now
			if uerr := s.store.UpdateMigration(ctx, *mrec); uerr != nil {
				s.log.Warn("could not mark cancelled drop-off migration", "code", code, "err", uerr)
			}
		}
	}

	s.audit(ctx, "migrate_dropoff_cancel", code, "success", "drop-off session cancelled", "")
	writeData(w, http.StatusOK, map[string]any{"ok": true})
}

// refreshDropoff applies expiry-on-read and the upload-readiness flip
// (waiting_for_upload -> uploaded) and persists any change, returning the updated
// record. Best-effort: a stat/store error leaves the record as read.
func (s *Server) refreshDropoff(ctx context.Context, rec *store.DropoffRecord) *store.DropoffRecord {
	switch migrate.DropStatus(rec.Status) {
	case migrate.DropCompleted, migrate.DropFailed, migrate.DropExpired, migrate.DropImporting:
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
	size, exists, err := s.drops.StatObject(ctx, rec.MetaKey)
	if err != nil {
		s.log.Warn("could not stat drop-off metadata on read", "code", rec.Code, "err", err)
		return rec
	}
	if !exists {
		return rec
	}
	_ = size // meta size itself is not surfaced; the dump size is.
	rec.Status = string(migrate.DropUploaded)
	if dumpSize, dumpExists, derr := s.drops.StatObject(ctx, rec.DumpKey); derr == nil && dumpExists {
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

// dropoffCommandDocker / dropoffCommandNative render the paste-able push command,
// mirroring scripts/install.sh's curl|sh one-liner. The presigned URLs are
// single-key, PUT-only, short-lived bearer tokens; the DBNAME / CONTAINER /
// SOURCE_HOST / POSTGRES_USER tokens are placeholders the operator fills in.
//
// The placeholders deliberately use plain UPPERCASE words rather than
// <angle-bracket> tokens: the common newbie mistake is pasting the line before
// substituting, and unquoted <…> would make the shell attempt input redirection
// ("No such file or directory") before sh ever reads the args. A literal DBNAME
// instead reaches migrate-push.sh, which catches it with a friendly placeholder
// guard. The password is NEVER a flag — migrate-push.sh reads it from PGPASSWORD
// or a /dev/tty prompt.
func dropoffCommandDocker(dumpURL, metaURL string) string {
	return fmt.Sprintf(
		"curl -fsSL %s | sh -s -- --dump-url '%s' --meta-url '%s' --db DBNAME --docker CONTAINER",
		migratePushScriptURL, dumpURL, metaURL)
}

func dropoffCommandNative(dumpURL, metaURL string) string {
	return fmt.Sprintf(
		"curl -fsSL %s | sh -s -- --dump-url '%s' --meta-url '%s' --db DBNAME --host SOURCE_HOST --port 5432 --user POSTGRES_USER",
		migratePushScriptURL, dumpURL, metaURL)
}
