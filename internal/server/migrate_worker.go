package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/migrate"
	"github.com/venkatesh-sekar/indiepg/internal/store"
)

// migrateWorkBaseDir is the base directory under which every migration job gets
// a private 0700 working directory (named by job id) for its dump files. It is
// cleared wholesale on panel startup so a crash mid-dump cannot leak disk.
const migrateWorkBaseDir = "/var/lib/indiepg/migrate"

// migrationJobTimeout bounds a single migration worker run. It is a generous
// backstop, not a tuning knob: an indie-hacker-scale dump/restore finishes well
// within it, but a source that accepts the connection then stalls mid-transfer
// (firewall black-hole, overloaded host, half-open TCP) can no longer wedge a
// job in "importing" forever — the worker context expires, the orchestrator
// records a failure, and the per-job work dir is cleaned up. The engine's
// PGCONNECT_TIMEOUT (see migrate.connArgs) catches the far more common
// connect-phase stall in seconds; this is the outer guard for a stall that
// begins after the connection is established. A var (not const) so tests can
// shrink it.
var migrationJobTimeout = 6 * time.Hour

// workerContext derives the bounded background context a detached migration
// worker runs under. Workers run on context.Background() (NOT the request
// context) so they survive the HTTP response; the timeout is the only thing
// standing between a stalled source and a job that hangs forever. The caller
// MUST defer the returned cancel.
func workerContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), migrationJobTimeout)
}

// jobWorkDir returns (and creates) the per-job temp directory for migration id.
// The directory holds the pg_dump output; the Orchestrator removes it on return.
// Creating it here (not in the Orchestrator) keeps the base dir constant and lets
// the startup sweep clear the whole tree.
//
// The directory chain is made 0711 (traverse, NOT list). A LOCAL restore runs
// pg_restore as the "postgres" OS user, which must descend INTO this dir to read
// the staged dump. Its ancestors default to 0700 — in particular the state dir
// /var/lib/indiepg is created 0700 to protect the 0600 SQLite file beside it —
// which blocks postgres entirely ("Permission denied"). 0711 lets postgres
// traverse to a known dump path without being able to enumerate the directory,
// and the 0600 state DB stays unreadable (the file mode, not the dir mode, guards
// it). The dump files themselves are written world-readable (0644) where pg_dump
// emits them (see engine.Dump) / where a downloaded dump is staged. MkdirAll does
// NOT relax dirs that already exist, so chmod every component of the chain
// explicitly.
func jobWorkDir(id int64) (string, error) {
	dir := filepath.Join(migrateWorkBaseDir, strconv.FormatInt(id, 10))
	if err := os.MkdirAll(dir, 0o711); err != nil {
		return "", core.InternalError("cannot create migration work dir %s", dir).Wrap(err)
	}
	for _, d := range []string{filepath.Dir(migrateWorkBaseDir), migrateWorkBaseDir, dir} {
		if err := os.Chmod(d, 0o711); err != nil {
			return "", core.InternalError("cannot make migration work dir %s traversable for the postgres restore user", d).Wrap(err)
		}
	}
	return dir, nil
}

// localTargetConn builds the ConnInfo for this panel's own Postgres, reached over
// the unix socket with peer auth (no password). The socket directory comes from
// the persisted config and the port is discovered live from the running server.
// Local() is true for the result, so the engine runs as the "postgres" OS user.
func (s *Server) localTargetConn(ctx context.Context) (migrate.ConnInfo, error) {
	port, err := s.pg.Port(ctx)
	if err != nil {
		return migrate.ConnInfo{}, err
	}
	socketDir := s.cfg.PGSocketDir
	if socketDir == "" {
		socketDir = "/var/run/postgresql"
	}
	return migrate.ConnInfo{Host: socketDir, Port: port}, nil
}

// storeRecorder adapts the local SQLite store to the migrate.Recorder sink so the
// Orchestrator can stream status/phase/progress/errors/row-counts into the
// migrations table without the migrate package importing internal/store. Every
// method reads the current record, mutates the relevant columns, and writes it
// back, so concurrent jobs (each with a distinct id) never clobber one another.
//
// The passed-in error/cause is already redacted by the Orchestrator (it only
// ever carries ConnInfo.Redacted() text, never a password), so Fail can persist
// cause.Error() verbatim.
type storeRecorder struct {
	store *store.Store
	id    int64
}

// newStoreRecorder builds a Recorder over the migration record with the given id.
func newStoreRecorder(st *store.Store, id int64) *storeRecorder {
	return &storeRecorder{store: st, id: id}
}

var _ migrate.Recorder = (*storeRecorder)(nil)

// Stage records a status + phase boundary.
func (r *storeRecorder) Stage(ctx context.Context, status migrate.Status, phase migrate.Phase) error {
	rec, err := r.store.GetMigration(ctx, r.id)
	if err != nil {
		return err
	}
	rec.Status = string(status)
	rec.Phase = string(phase)
	return r.store.UpdateMigration(ctx, *rec)
}

// Progress records a coarse done/total step count and the total bytes dumped.
func (r *storeRecorder) Progress(ctx context.Context, done, total, bytesTotal int64) error {
	rec, err := r.store.GetMigration(ctx, r.id)
	if err != nil {
		return err
	}
	rec.ProgressDone = done
	rec.ProgressTotal = total
	rec.BytesTotal = bytesTotal
	return r.store.UpdateMigration(ctx, *rec)
}

// Fail marks the migration failed with the (already redacted) cause and a
// finished timestamp. The terminal write runs on a context detached from the
// worker's cancellation/deadline (then re-bounded): the most common reason to
// record a failure is the worker context EXPIRING on a stalled source, and a
// cancelled context would otherwise make the store write a no-op — leaving the
// job wedged in "importing" forever, the exact bug the worker timeout fixes.
func (r *storeRecorder) Fail(ctx context.Context, cause error) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	rec, err := r.store.GetMigration(ctx, r.id)
	if err != nil {
		return err
	}
	rec.Status = string(migrate.StatusFailed)
	rec.Phase = ""
	if cause != nil {
		rec.Error = failErrorText(cause)
	}
	now := time.Now().UTC()
	rec.FinishedAt = &now
	return r.store.UpdateMigration(ctx, *rec)
}

// maxPersistedDiagnostic bounds the command diagnostic appended to a failed
// migration's persisted error, so a pathological multi-megabyte stderr cannot bloat
// the row; the actionable pg_restore reason is in the first lines.
const maxPersistedDiagnostic = 2000

// failErrorText builds the error string persisted for a failed migration: the
// (already password-redacted) cause, plus — when the cause carries a structured
// "stderr" diagnostic (the actual pg_restore/psql failure reason) — a bounded summary
// of it. Without this an operator sees only a generic "exit status 1" while the
// actionable PostgreSQL reason sits unread in Error.Details. The diagnostic is re-run
// through SanitizeRestoreStderr (idempotent) as defense in depth, so even an stderr
// detail that was only password-scrubbed at its source never persists an echoed
// "Command was:" DDL body — which can embed a secret — while keeping pg_restore's
// error:/fatal: reason lines.
func failErrorText(cause error) string {
	msg := cause.Error()
	pe, ok := core.AsError(cause)
	if !ok {
		return msg
	}
	raw, ok := pe.Details["stderr"].(string)
	if !ok {
		return msg
	}
	diag := boundDiagnostic(strings.TrimSpace(migrate.SanitizeRestoreStderr(raw)))
	if diag == "" || strings.Contains(msg, diag) {
		return msg
	}
	return msg + "\n" + diag
}

// boundDiagnostic caps a diagnostic string at maxPersistedDiagnostic, on a rune
// boundary so a multi-byte character is never split.
func boundDiagnostic(s string) string {
	if len(s) <= maxPersistedDiagnostic {
		return s
	}
	r := []rune(s)
	if len(r) <= maxPersistedDiagnostic {
		return s
	}
	return strings.TrimSpace(string(r[:maxPersistedDiagnostic])) + " … (truncated)"
}

// Succeed marks the migration completed with the verified row counts and a
// finished timestamp. Like Fail, the terminal write is detached from the worker
// context and re-bounded so a shutdown racing the final write cannot leave a
// genuinely-completed migration stuck displaying "importing".
func (r *storeRecorder) Succeed(ctx context.Context, src, tgt map[string]int64) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	rec, err := r.store.GetMigration(ctx, r.id)
	if err != nil {
		return err
	}
	rec.Status = string(migrate.StatusCompleted)
	rec.Phase = ""
	rec.Error = ""
	rec.RowCountsSrc = marshalCounts(src)
	rec.RowCountsTgt = marshalCounts(tgt)
	now := time.Now().UTC()
	rec.FinishedAt = &now
	return r.store.UpdateMigration(ctx, *rec)
}

// marshalCounts JSON-encodes a "schema.table" -> count map, falling back to an
// empty object on a nil map or (impossible for this type) an encode error.
func marshalCounts(m map[string]int64) string {
	if len(m) == 0 {
		return "{}"
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// runDirectJob is the background worker for a direct-pull migration. It runs on a
// context.Background()-derived context (NOT the request context) so it survives
// the HTTP response, builds a fresh Orchestrator with a per-job work dir, and
// drives Direct. All failures are recorded by the Orchestrator via the recorder;
// this wrapper only logs the terminal outcome.
func (s *Server) runDirectJob(id int64, job migrate.Job) {
	ctx, cancel := workerContext()
	defer cancel()
	// Release the process-local target claim startDirectJob acquired, so the next
	// import into this local target is admitted once this worker exits.
	defer s.releaseImportTarget(importTargetKey(job))
	rec := newStoreRecorder(s.store, id)

	// Resolve the local target here (it needs a live Postgres for the port) so a
	// momentarily-unreachable Postgres fails the job cleanly instead of the request.
	target, err := s.localTargetConn(ctx)
	if err != nil {
		_ = rec.Fail(ctx, core.InternalError("cannot reach local Postgres to restore into").Wrap(err))
		return
	}
	job.Target = target

	workDir, err := jobWorkDir(id)
	if err != nil {
		_ = rec.Fail(ctx, err)
		return
	}
	// Direct pull needs no S3 Service; snapshot it once anyway for a consistent read.
	orch := migrate.NewOrchestrator(s.migrateEngine, s.migrateService(), nil, workDir, s.log)
	if derr := orch.Direct(ctx, job, rec); derr != nil {
		s.log.Warn("direct migration failed", "id", id, "err", derr)
	}
}

// runExportJob is the background worker for the ssh-less SOURCE side: it dumps
// the requested database and uploads it to the shared S3 bucket, advancing the
// session. It requires the S3-backed Service (s.migrate must be non-nil; the
// caller has already verified that).
func (s *Server) runExportJob(id int64, sess *migrate.MigrationSession, src migrate.ConnInfo) {
	ctx, cancel := workerContext()
	defer cancel()
	rec := newStoreRecorder(s.store, id)

	// Snapshot the S3-backed Service once for a consistent read against a possible
	// concurrent config save. The caller verified it was non-nil at session start.
	svc := s.migrateService()
	if svc == nil {
		_ = rec.Fail(ctx, errSSHLessRequiresS3())
		return
	}

	workDir, err := jobWorkDir(id)
	if err != nil {
		_ = rec.Fail(ctx, err)
		return
	}
	orch := migrate.NewOrchestrator(s.migrateEngine, svc, svc.ObjectStore(), workDir, s.log)
	if eerr := orch.ExportToSession(ctx, sess, src, rec); eerr != nil {
		s.log.Warn("ssh-less export failed", "id", id, "code", sess.Code, "err", eerr)
	}
}

// runImportWorker is the background worker for the ssh-less TARGET side: it polls
// the shared session document until the source has finished exporting (or the
// session fails/expires), then downloads, restores, and verifies the dump into
// the local Postgres. It requires the S3-backed Service.
func (s *Server) runImportWorker(id int64, code, targetDB string) {
	ctx, cancel := workerContext()
	defer cancel()
	// Release the process-local target claim handleCreateMigrationSession acquired.
	defer s.releaseImportTarget(targetDB)
	rec := newStoreRecorder(s.store, id)

	// Snapshot the S3-backed Service once so the whole poll/import runs against one
	// consistent transport even if a config save swaps it mid-flight.
	svc := s.migrateService()
	if svc == nil {
		_ = rec.Fail(ctx, errSSHLessRequiresS3())
		return
	}

	tgt, err := s.localTargetConn(ctx)
	if err != nil {
		_ = rec.Fail(ctx, core.InternalError("cannot reach local Postgres to restore into").Wrap(err))
		return
	}

	// Poll the cross-panel session until the dump is exported and ready to import,
	// or the session reaches a terminal/non-importable state. The local record is
	// the source of truth for this panel; the session is only the channel.
	const pollInterval = 3 * time.Second
	deadline := time.Now().Add(migrate.DefaultTTL)
	for {
		if time.Now().After(deadline) {
			_ = rec.Fail(ctx, core.InternalError("timed out waiting for source to export session %s", code))
			return
		}
		sess, err := svc.GetSession(ctx, code)
		if err != nil {
			_ = rec.Fail(ctx, err)
			return
		}
		switch sess.Status {
		case migrate.StatusExported:
			workDir, werr := jobWorkDir(id)
			if werr != nil {
				_ = rec.Fail(ctx, werr)
				return
			}
			orch := migrate.NewOrchestrator(s.migrateEngine, svc, svc.ObjectStore(), workDir, s.log)
			if ierr := orch.ImportFromSession(ctx, sess, tgt, rec); ierr != nil {
				s.log.Warn("ssh-less import failed", "id", id, "code", code, "err", ierr)
			}
			return
		case migrate.StatusFailed, migrate.StatusExpired:
			_ = rec.Fail(ctx, core.InternalError("session %s ended in state %q before import", code, sess.Status))
			return
		default:
			// waiting_for_export / exporting / importing: keep waiting.
		}
		select {
		case <-ctx.Done():
			_ = rec.Fail(ctx, ctx.Err())
			return
		case <-time.After(pollInterval):
		}
	}
}

// migrationResponse is the wire shape of a migration record. It is the local
// store's MigrationRecord with the row-count JSON blobs decoded into maps so the
// SPA does not have to double-parse.
type migrationResponse struct {
	ID             int64            `json:"id"`
	Mode           string           `json:"mode"`
	Role           string           `json:"role"`
	Status         string           `json:"status"`
	Phase          string           `json:"phase"`
	SourceSummary  string           `json:"source_summary"`
	TargetDatabase string           `json:"target_database"`
	Overwrite      bool             `json:"overwrite"`
	Code           string           `json:"code"`
	ProgressDone   int64            `json:"progress_done"`
	ProgressTotal  int64            `json:"progress_total"`
	BytesTotal     int64            `json:"bytes_total"`
	Error          string           `json:"error"`
	RowCountsSrc   map[string]int64 `json:"row_counts_src"`
	RowCountsTgt   map[string]int64 `json:"row_counts_tgt"`
	CreatedAt      time.Time        `json:"created_at"`
	UpdatedAt      time.Time        `json:"updated_at"`
	FinishedAt     *time.Time       `json:"finished_at,omitempty"`
}

// toMigrationResponse maps a store record to its wire shape, decoding the
// row-count JSON blobs into maps (a malformed blob degrades to an empty map
// rather than failing the read).
func toMigrationResponse(m store.MigrationRecord) migrationResponse {
	return migrationResponse{
		ID:             m.ID,
		Mode:           m.Mode,
		Role:           m.Role,
		Status:         m.Status,
		Phase:          m.Phase,
		SourceSummary:  m.SourceSummary,
		TargetDatabase: m.TargetDatabase,
		Overwrite:      m.Overwrite,
		Code:           m.Code,
		ProgressDone:   m.ProgressDone,
		ProgressTotal:  m.ProgressTotal,
		BytesTotal:     m.BytesTotal,
		Error:          m.Error,
		RowCountsSrc:   unmarshalCounts(m.RowCountsSrc),
		RowCountsTgt:   unmarshalCounts(m.RowCountsTgt),
		CreatedAt:      m.CreatedAt,
		UpdatedAt:      m.UpdatedAt,
		FinishedAt:     m.FinishedAt,
	}
}

// unmarshalCounts decodes a "schema.table" -> count JSON object, returning an
// empty (non-nil) map for empty or malformed input so the field serializes as
// {} rather than null.
func unmarshalCounts(s string) map[string]int64 {
	out := map[string]int64{}
	if s == "" {
		return out
	}
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return map[string]int64{}
	}
	return out
}

// migrationSummaryFor builds a short human label for an audit summary line.
func migrationSummaryFor(mode migrate.Mode, redactedSource string) string {
	return fmt.Sprintf("%s migration from %s", mode, redactedSource)
}

// storeMigrationRecord builds the initial local record for a direct-pull job. It
// records the redacted source summary (never the password) and starts the job in
// the importing/validating state the worker advances from.
func storeMigrationRecord(job migrate.Job) store.MigrationRecord {
	return store.MigrationRecord{
		Mode:           string(job.Mode),
		Role:           "direct",
		Status:         string(migrate.StatusImporting),
		Phase:          string(migrate.PhaseValidating),
		SourceSummary:  job.Source.Redacted(),
		TargetDatabase: job.TargetDatabase,
		Overwrite:      job.Overwrite,
	}
}

// sessionMigrationRecord builds the initial local record for an ssh-less session
// (target role by default; callers override Role/SourceSummary for the source
// side). It carries the shared code so GetMigrationByCode can find it later.
func sessionMigrationRecord(code, database string) store.MigrationRecord {
	return store.MigrationRecord{
		Mode:           string(migrate.ModeSession),
		Role:           "target",
		Status:         string(migrate.StatusWaiting),
		Phase:          "",
		TargetDatabase: database,
		Code:           code,
	}
}

// parseIDParam reads a positive int64 path parameter, returning a CodeValidation
// error when it is missing or malformed.
func parseIDParam(r *http.Request, name string) (int64, error) {
	raw := chi.URLParam(r, name)
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, core.ValidationError("invalid %s %q", name, raw)
	}
	return id, nil
}

// nowUTC returns the current time in UTC (a tiny helper so handlers can take a
// *time.Time without an inline temporary).
func nowUTC() time.Time {
	return time.Now().UTC()
}
