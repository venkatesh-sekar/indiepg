package server

import (
	"context"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/migrate"
)

// sourceConnRequest is the user-supplied source Postgres connection for a
// direct-pull migration. The Password is read from the request but is NEVER
// persisted to the store, logged, or placed in any error/audit text — only the
// redacted "user@host:port/db" form (ConnInfo.Redacted()) is ever surfaced.
type sourceConnRequest struct {
	Host     string `json:"host"`
	Port     string `json:"port"`
	User     string `json:"user"`
	Password string `json:"password"`
	Database string `json:"database"`
	SSLMode  string `json:"sslmode"`
}

// toConnInfo converts the wire shape to a migrate.ConnInfo.
func (c sourceConnRequest) toConnInfo() migrate.ConnInfo {
	return migrate.ConnInfo{
		Host:     c.Host,
		Port:     c.Port,
		User:     c.User,
		Password: c.Password,
		SSLMode:  c.SSLMode,
		Database: c.Database,
	}
}

// singleDBRequest drives a direct single-database pull: dump one database from
// the source and restore it into TargetDatabase on this panel's Postgres.
type singleDBRequest struct {
	Source         sourceConnRequest `json:"source"`
	TargetDatabase string            `json:"target_database"`
	Overwrite      bool              `json:"overwrite"`
	Confirm        string            `json:"confirm"`
}

// clusterRequest drives a direct whole-cluster pull: every non-template source
// database plus globals, restored into this panel's Postgres.
type clusterRequest struct {
	Source    sourceConnRequest `json:"source"`
	Overwrite bool              `json:"overwrite"`
	Exclude   []string          `json:"exclude"`
	// Confirm gates the most destructive operation in the feature: a whole-cluster
	// overwrite drops EVERY matching local database. Because there is no single
	// database name to echo, the operator must type the fixed sentinel
	// clusterOverwriteConfirm ("OVERWRITE") to authorize it.
	Confirm string `json:"confirm"`
}

// clusterOverwriteConfirm is the fixed phrase an operator must type to authorize
// a destructive whole-cluster overwrite (which can drop every matching local
// database). A bare boolean is never sufficient.
const clusterOverwriteConfirm = "OVERWRITE"

// createSessionRequest creates the ssh-less TARGET session (S3 channel) for a
// named database. The 6-char code it returns is shared with the source panel.
type createSessionRequest struct {
	Database string `json:"database"`
}

// exportSessionRequest joins an ssh-less session as the SOURCE and starts the
// export of the named database to the shared bucket.
type exportSessionRequest struct {
	Source   sourceConnRequest `json:"source"`
	Database string            `json:"database"`
}

// migrateStartedResponse is the immediate response for an async migration job:
// the local record id the SPA polls plus the initial status.
type migrateStartedResponse struct {
	ID     int64  `json:"id"`
	Status string `json:"status"`
}

// handleMigrateSingleDB starts a DIRECT single-database pull. It needs NO S3: the
// panel runs pg_dump against the user-supplied source and pg_restore into its own
// Postgres. It validates input, inserts a local record (the source of truth the
// SPA polls), spawns the background worker on a context that survives the
// response, and returns the record id immediately. POST /migrate/single-db.
func (s *Server) handleMigrateSingleDB(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req singleDBRequest
	if err := decodeJSON(r, &req, maxBodyBytes); err != nil {
		writeError(w, err)
		return
	}

	source := req.Source.toConnInfo()
	if err := validateDirectSource(source, true); err != nil {
		s.audit(ctx, "migrate_single_db", req.TargetDatabase, "failure", "invalid source connection", core.CodeOf(err))
		writeError(w, err)
		return
	}
	if err := core.ValidateIdentifier(req.TargetDatabase, "database"); err != nil {
		s.audit(ctx, "migrate_single_db", req.TargetDatabase, "failure", "invalid target database", core.CodeOf(err))
		writeError(w, err)
		return
	}

	// A destructive overwrite must be authorized by re-typing the target database
	// name server-side, not by a bare boolean. A checkbox alone can never drop a
	// non-empty target: when Overwrite is set the operator must echo TargetDatabase
	// in Confirm, otherwise this returns a typed *core.SafetyError (CodeSafety) and
	// no job is started. (The orchestrator's non-empty refusal remains a second
	// line of defence for the case where the operator did NOT opt into overwrite.)
	if req.Overwrite {
		if err := core.RequireConfirmation("overwrite database "+req.TargetDatabase, req.TargetDatabase, req.Confirm); err != nil {
			s.audit(ctx, "migrate_single_db", req.TargetDatabase, "failure", "overwrite not confirmed", core.CodeOf(err))
			writeError(w, err)
			return
		}
	}

	// The target is this panel's local Postgres; it is resolved inside the worker
	// (it needs a live Postgres for the port) so the handler can record the job and
	// return an id even when Postgres is momentarily unreachable — the worker then
	// fails the job with a clear error rather than the request failing opaquely.
	job := migrate.Job{
		Mode:           migrate.ModeSingleDB,
		Source:         source,
		TargetDatabase: req.TargetDatabase,
		Overwrite:      req.Overwrite,
	}
	id, err := s.startDirectJob(ctx, job)
	if err != nil {
		s.audit(ctx, "migrate_single_db", req.TargetDatabase, "failure", "could not record migration", core.CodeOf(err))
		writeError(w, err)
		return
	}

	s.audit(ctx, "migrate_single_db", req.TargetDatabase, "success",
		migrationSummaryFor(migrate.ModeSingleDB, source.Redacted()), "")
	writeData(w, http.StatusAccepted, migrateStartedResponse{ID: id, Status: string(migrate.StatusImporting)})
}

// handleMigrateCluster starts a DIRECT whole-cluster pull. Like single-db it
// needs NO S3. POST /migrate/cluster.
func (s *Server) handleMigrateCluster(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req clusterRequest
	if err := decodeJSON(r, &req, maxBodyBytes); err != nil {
		writeError(w, err)
		return
	}

	source := req.Source.toConnInfo()
	// Cluster mode targets the whole instance, so no source database is required.
	if err := validateDirectSource(source, false); err != nil {
		s.audit(ctx, "migrate_cluster", source.Redacted(), "failure", "invalid source connection", core.CodeOf(err))
		writeError(w, err)
		return
	}

	// A whole-cluster overwrite drops every matching local database, so it requires
	// the operator to type the fixed sentinel; a bare boolean can never authorize
	// it. (directCluster additionally refuses any non-empty target when Overwrite is
	// NOT set, mirroring the single-db gate.)
	if req.Overwrite {
		if err := core.RequireConfirmation("overwrite all databases on this panel", clusterOverwriteConfirm, req.Confirm); err != nil {
			s.audit(ctx, "migrate_cluster", source.Redacted(), "failure", "overwrite not confirmed", core.CodeOf(err))
			writeError(w, err)
			return
		}
	}

	// The target (this panel's local Postgres) is resolved inside the worker; see
	// the single-db handler for why.
	job := migrate.Job{
		Mode:      migrate.ModeCluster,
		Source:    source,
		Overwrite: req.Overwrite,
		Exclude:   req.Exclude,
	}
	id, err := s.startDirectJob(ctx, job)
	if err != nil {
		s.audit(ctx, "migrate_cluster", source.Redacted(), "failure", "could not record migration", core.CodeOf(err))
		writeError(w, err)
		return
	}

	s.audit(ctx, "migrate_cluster", source.Redacted(), "success",
		migrationSummaryFor(migrate.ModeCluster, source.Redacted()), "")
	writeData(w, http.StatusAccepted, migrateStartedResponse{ID: id, Status: string(migrate.StatusImporting)})
}

// startDirectJob inserts the local migration record and spawns the direct-pull
// worker on a background context. It returns the record id. The Source.Redacted()
// summary is the only source detail persisted — never the password.
func (s *Server) startDirectJob(ctx context.Context, job migrate.Job) (int64, error) {
	rec := storeMigrationRecord(job)
	id, err := s.store.InsertMigration(ctx, rec)
	if err != nil {
		return 0, err
	}
	go s.runDirectJob(id, job)
	return id, nil
}

// handleListMigrations returns the migration history, newest first. Read-only;
// no audit. GET /migrate.
func (s *Server) handleListMigrations(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	recs, err := s.store.ListMigrations(ctx, 50)
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]migrationResponse, 0, len(recs))
	for _, m := range recs {
		out = append(out, toMigrationResponse(m))
	}
	writeData(w, http.StatusOK, map[string]any{"migrations": out})
}

// handleGetMigration returns one migration record by id so the SPA can poll a
// direct job's progress. Read-only; no audit. GET /migrate/{id}.
func (s *Server) handleGetMigration(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := parseIDParam(r, "id")
	if err != nil {
		writeError(w, err)
		return
	}
	rec, err := s.store.GetMigration(ctx, id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, toMigrationResponse(*rec))
}

// handleCreateMigrationSession creates an ssh-less TARGET session over the shared
// S3 bucket. This is the ONLY honest "requires S3" path: when no S3 target is
// configured s.migrate is nil and we return a typed CodeInternal error pointing
// the operator at either configuring S3 or using the direct pull (which needs
// none). POST /migrate/sessions.
func (s *Server) handleCreateMigrationSession(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if s.migrate == nil {
		writeError(w, errSSHLessRequiresS3())
		return
	}

	var req createSessionRequest
	if err := decodeJSON(r, &req, maxBodyBytes); err != nil {
		writeError(w, err)
		return
	}

	sess, err := s.migrate.CreateSession(ctx, req.Database, migrate.DefaultTTL)
	if err != nil {
		s.audit(ctx, "migrate_session_create", req.Database, "failure", "create session failed", core.CodeOf(err))
		writeError(w, err)
		return
	}

	rec := sessionMigrationRecord(sess.Code, sess.Database)
	id, err := s.store.InsertMigration(ctx, rec)
	if err != nil {
		s.audit(ctx, "migrate_session_create", req.Database, "failure", "could not record migration", core.CodeOf(err))
		writeError(w, err)
		return
	}

	// The target side waits for the source to export, then imports. It resolves
	// the local target Postgres itself so the request returns immediately.
	go s.runImportWorker(id, sess.Code)

	s.audit(ctx, "migrate_session_create", sess.Code, "success", "ssh-less target session created", "")
	writeData(w, http.StatusCreated, sess)
}

// handleGetMigrationSession returns the ssh-less session document by code (the
// cross-panel channel) and best-effort mirrors its status into the local record.
// Requires S3. GET /migrate/sessions/{code}.
func (s *Server) handleGetMigrationSession(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if s.migrate == nil {
		writeError(w, errSSHLessRequiresS3())
		return
	}
	code := chi.URLParam(r, "code")
	sess, err := s.migrate.GetSession(ctx, code)
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, sess)
}

// handleExportMigrationSession joins an ssh-less session as the SOURCE and starts
// the export of the named database to the shared bucket. Requires S3.
// POST /migrate/sessions/{code}/export.
func (s *Server) handleExportMigrationSession(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if s.migrate == nil {
		writeError(w, errSSHLessRequiresS3())
		return
	}
	code := chi.URLParam(r, "code")

	var req exportSessionRequest
	if err := decodeJSON(r, &req, maxBodyBytes); err != nil {
		writeError(w, err)
		return
	}

	source := req.Source.toConnInfo()
	if req.Database != "" {
		source.Database = req.Database
	}
	if err := validateDirectSource(source, true); err != nil {
		s.audit(ctx, "migrate_session_export", code, "failure", "invalid source connection", core.CodeOf(err))
		writeError(w, err)
		return
	}

	sess, err := s.migrate.GetSession(ctx, code)
	if err != nil {
		s.audit(ctx, "migrate_session_export", code, "failure", "session not found", core.CodeOf(err))
		writeError(w, err)
		return
	}

	rec := sessionMigrationRecord(sess.Code, source.Database)
	rec.Role = "source"
	rec.SourceSummary = source.Redacted()
	id, err := s.store.InsertMigration(ctx, rec)
	if err != nil {
		s.audit(ctx, "migrate_session_export", code, "failure", "could not record migration", core.CodeOf(err))
		writeError(w, err)
		return
	}

	go s.runExportJob(id, sess, source)

	s.audit(ctx, "migrate_session_export", code, "success",
		migrationSummaryFor(migrate.ModeSession, source.Redacted()), "")
	writeData(w, http.StatusAccepted, migrateStartedResponse{ID: id, Status: string(migrate.StatusExporting)})
}

// handleCancelMigrationSession cancels an ssh-less session: it deletes the shared
// session document and dump and marks the local record failed. Requires S3.
// DELETE /migrate/sessions/{code}.
func (s *Server) handleCancelMigrationSession(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if s.migrate == nil {
		writeError(w, errSSHLessRequiresS3())
		return
	}
	code := chi.URLParam(r, "code")

	if err := s.migrate.CleanupSession(ctx, code); err != nil {
		s.audit(ctx, "migrate_session_cancel", code, "failure", "cleanup failed", core.CodeOf(err))
		writeError(w, err)
		return
	}

	// Best-effort: mark the local record for this code failed/cancelled so the UI
	// reflects the cancellation. A missing record is not an error.
	if rec, err := s.store.GetMigrationByCode(ctx, code); err == nil {
		if rec.Status != string(migrate.StatusCompleted) {
			rec.Status = string(migrate.StatusFailed)
			rec.Phase = ""
			rec.Error = "cancelled"
			now := nowUTC()
			rec.FinishedAt = &now
			if uerr := s.store.UpdateMigration(ctx, *rec); uerr != nil {
				s.log.Warn("could not mark cancelled migration record", "code", code, "err", uerr)
			}
		}
	}

	s.audit(ctx, "migrate_session_cancel", code, "success", "ssh-less session cancelled", "")
	writeData(w, http.StatusOK, map[string]any{"ok": true})
}

// errSSHLessRequiresS3 is the single, honest "S3 required" error. It is returned
// ONLY by the ssh-less handshake endpoints when no S3 target is configured; the
// direct pull endpoints never produce it. The hint points the operator at the
// no-S3 alternative.
func errSSHLessRequiresS3() error {
	return core.InternalError("cross-panel (ssh-less) migration requires S3 object storage").
		WithHint("configure an S3 backup target in Settings, or use Direct pull which needs no S3")
}

// validateDirectSource validates a user-supplied source connection for a direct
// pull or ssh-less export. A source must be remote (a real host); a missing host
// is rejected so the panel never tries to "migrate" from itself by accident.
// requireDatabase gates whether a source database name is mandatory (single-db
// and export require it; cluster does not).
func validateDirectSource(c migrate.ConnInfo, requireDatabase bool) error {
	if c.Host == "" {
		return core.ValidationError("source host is required").
			WithHint("enter the source Postgres host (and port/credentials) to pull from")
	}
	if requireDatabase {
		if c.Database == "" {
			return core.ValidationError("source database is required")
		}
		if err := core.ValidateIdentifier(c.Database, "database"); err != nil {
			return err
		}
	}
	if err := validateSourcePort(c.Port); err != nil {
		return err
	}
	if err := validateSourceUser(c.User); err != nil {
		return err
	}
	if err := validateSourceSSLMode(c.SSLMode); err != nil {
		return err
	}
	return nil
}

// validateSourcePort rejects a non-numeric or out-of-range source port. The port
// is a string carried verbatim into the `-p <port>` libpq argv (value position,
// no shell — never an injection); validating it just turns an opaque connect-time
// libpq failure into a clear up-front error. Empty is allowed (defaults to 5432).
func validateSourcePort(port string) error {
	if port == "" {
		return nil
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return core.ValidationError("source port must be a number between 1 and 65535").
			WithHint("enter the source Postgres port, e.g. 5432, or leave it blank")
	}
	return nil
}

// validateSourceUser rejects a source role name carrying a control character or
// exceeding Postgres's 63-byte identifier limit. The user flows into the `-U
// <user>` libpq argv (value position, no shell — never an injection); the guard
// keeps a stray newline/NUL out of the argv and fails a too-long name fast with a
// clear message instead of a confusing libpq error. Empty is allowed (libpq then
// defaults to the OS user). Role names legitimately contain mixed case and
// symbols, so only control characters — never ordinary punctuation — are rejected.
func validateSourceUser(user string) error {
	if user == "" {
		return nil
	}
	if len(user) > 63 {
		return core.ValidationError("source user name is too long (max 63 bytes)")
	}
	for _, r := range user {
		if r < 0x20 || r == 0x7f {
			return core.ValidationError("source user name contains a control character")
		}
	}
	return nil
}

// allowedSSLModes is the exact set of libpq sslmode values. Validating against it
// (PGSSLMODE flows into the client env in value position, no shell) turns a typo
// like "yes" into a clear up-front error rather than an opaque libpq connect
// failure. Empty is allowed — libpq then applies its own default (prefer).
var allowedSSLModes = map[string]bool{
	"disable":     true,
	"allow":       true,
	"prefer":      true,
	"require":     true,
	"verify-ca":   true,
	"verify-full": true,
}

func validateSourceSSLMode(mode string) error {
	if mode == "" {
		return nil
	}
	if !allowedSSLModes[mode] {
		return core.ValidationError("source sslmode must be one of disable, allow, prefer, require, verify-ca, verify-full").
			WithHint("leave sslmode blank to use the libpq default")
	}
	return nil
}
