package server

import (
	"context"
	"net/http"
	"time"

	"github.com/venkatesh-sekar/indiepg/internal/backup"
	"github.com/venkatesh-sekar/indiepg/internal/config"
	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/store"
)

// backupHistoryResponse is the read-only history payload for the backups page:
// persisted run records plus restore-test results, newest first.
type backupHistoryResponse struct {
	Backups      []backupRecordResponse      `json:"backups"`
	RestoreTests []restoreTestRecordResponse `json:"restore_tests"`
}

// backupRecordResponse mirrors store.BackupRecord on the wire.
type backupRecordResponse struct {
	ID            int64      `json:"id"`
	Label         string     `json:"label"`
	BackupType    string     `json:"backup_type"`
	StartedAt     time.Time  `json:"started_at"`
	StoppedAt     *time.Time `json:"stopped_at"`
	SizeBytes     int64      `json:"size_bytes"`
	DatabaseBytes int64      `json:"database_bytes"`
	RepoBytes     int64      `json:"repo_bytes"`
	WALStart      string     `json:"wal_start"`
	WALStop       string     `json:"wal_stop"`
	Result        string     `json:"result"`
	RepoPath      string     `json:"repo_path"`
	Error         string     `json:"error"`
}

// restoreTestRecordResponse mirrors store.RestoreTestRecord on the wire.
type restoreTestRecordResponse struct {
	ID           int64     `json:"id"`
	TestedAt     time.Time `json:"tested_at"`
	SourceLabel  string    `json:"source_label"`
	VerifiedRows int64     `json:"verified_rows"`
	Result       string    `json:"result"`
	DurationMS   int64     `json:"duration_ms"`
	Detail       string    `json:"detail"`
}

// runBackupRequest selects which pgBackRest backup type to run.
type runBackupRequest struct {
	Type string `json:"type"`
}

// runBackupStartedResponse is the async start acknowledgement: the new history
// row id and its initial "running" state, so the SPA can close the dialog and
// poll backup history for completion instead of holding the request open for the
// entire run.
type runBackupStartedResponse struct {
	ID     int64  `json:"id"`
	Type   string `json:"type"`
	Result string `json:"result"`
}

// restoreRequest drives a guarded restore. Target is optional (nil => recover to
// latest WAL). Confirm carries the typed-name confirmation the foundation
// requires before overwriting the live cluster.
type restoreRequest struct {
	Target  *restoreTargetRequest `json:"target"`
	Delta   bool                  `json:"delta"`
	Confirm string                `json:"confirm"`
}

// restoreTargetRequest is the optional point-in-time-recovery target. Exactly
// one of time/xid/lsn/name selects where recovery stops (validated downstream);
// all empty means recover to the latest available WAL.
type restoreTargetRequest struct {
	Time   string `json:"time"`
	XID    string `json:"xid"`
	LSN    string `json:"lsn"`
	Name   string `json:"name"`
	Action string `json:"action"`
}

// handleListBackups returns the persisted backup run history and restore-test
// results. It is read-only and does not audit. The live pgBackRest repo view is
// intentionally not consulted here so the page renders even when pgbackrest or
// the stanza is unavailable.
func (s *Server) handleListBackups(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	backups, err := s.store.ListBackups(ctx, 50)
	if err != nil {
		writeError(w, err)
		return
	}
	tests, err := s.store.ListRestoreTests(ctx, 50)
	if err != nil {
		writeError(w, err)
		return
	}

	resp := backupHistoryResponse{
		Backups:      mapBackupRecords(backups),
		RestoreTests: mapRestoreTestRecords(tests),
	}
	writeData(w, http.StatusOK, resp)
}

// handleRunBackup starts a pgBackRest backup of the requested type and returns
// immediately with the new history row id (202 Accepted). A backup may run for
// minutes — hours for a full — so running it inside the request held the
// connection open with no signal and looked hung; instead StartBackup runs the
// fast gates synchronously (config self-heal, single-flight, ownership) so a
// misconfig/conflict/foreign-owner still returns a clean inline error, then runs
// pgBackRest in the background. The SPA polls backup history for the "running"
// row to transition to success/fail.
func (s *Server) handleRunBackup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req runBackupRequest
	if err := decodeJSON(r, &req, maxBodyBytes); err != nil {
		writeError(w, err)
		return
	}

	t, err := backup.ParseType(req.Type)
	if err != nil {
		writeError(w, err)
		return
	}

	// Self-heal the pgBackRest config before running: a backup has an implicit
	// prerequisite (a managed config with the stanza's pg1-path, and an
	// initialized repo) that nothing else on this path guarantees — e.g. a panel
	// that started before Postgres was reachable never wrote it. Provision it now
	// from the persisted config so the operator gets a clear, actionable error
	// here instead of pgBackRest's cryptic "requires option: pg1-path".
	cfg, err := config.Load(ctx, s.store)
	if err != nil {
		writeError(w, err)
		return
	}
	if _, err := s.ensureBackupConfigured(ctx, cfg); err != nil {
		s.audit(ctx, "run_backup", req.Type, "failure", "configure pgBackRest before backup failed", core.CodeOf(err))
		writeError(w, err)
		return
	}

	id, err := s.backups.StartBackup(ctx, t)
	if err != nil {
		s.audit(ctx, "run_backup", req.Type, "failure", "backup failed to start", core.CodeOf(err))
		writeError(w, err)
		return
	}

	s.audit(ctx, "run_backup", req.Type, "success", "backup started", "")
	writeData(w, http.StatusAccepted, runBackupStartedResponse{ID: id, Type: req.Type, Result: "running"})
}

// handleRestore performs a guarded restore. The foundation method takes a
// pre-restore safety backup and requires typed-name confirmation before
// overwriting the live cluster; the request supplies that confirmation and an
// optional point-in-time-recovery target.
//
// The restore runs on a context detached from the HTTP request so a client
// disconnect (browser tab close, network drop, proxy timeout) cannot cancel
// pgBackRest mid-write and leave the data directory in an inconsistent state.
func (s *Server) handleRestore(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req restoreRequest
	if err := decodeJSON(r, &req, maxBodyBytes); err != nil {
		writeError(w, err)
		return
	}

	target, err := buildRecoveryTarget(req.Target)
	if err != nil {
		writeError(w, err)
		return
	}

	restoreCtx := context.WithoutCancel(ctx)
	res, err := s.backups.Restore(restoreCtx, target, req.Delta, req.Confirm)
	if err != nil {
		s.audit(ctx, "restore", "stanza", "failure", "restore failed", core.CodeOf(err))
		writeError(w, err)
		return
	}

	s.audit(ctx, "restore", "stanza", "success", "restore completed", "")
	writeData(w, http.StatusOK, res)
}

// handleRestoreTest proves a backup is recoverable. By default it runs the
// cheap, always-safe `pgbackrest verify` (a read-only repo integrity check that
// never touches the live data dir and never restores). With the explicit opt-in
// `?deep=true` it instead runs a full scratch restore + boot, which catches
// recovery-time failures verify cannot but is heavier and requires disk
// headroom. The pass/fail outcome is recorded in restore-test history so the
// durability surfacing can answer "have my backups been proven recoverable?".
func (s *Server) handleRestoreTest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	deep := r.URL.Query().Get("deep") == "true"

	var (
		res core.Result
		err error
	)
	if deep {
		res, err = s.backups.RestoreTestDeep(ctx)
	} else {
		res, err = s.backups.RestoreTest(ctx)
	}
	if err != nil {
		s.audit(ctx, "restore_test", "stanza", "failure", "restore-test failed", core.CodeOf(err))
		writeError(w, err)
		return
	}

	s.audit(ctx, "restore_test", "stanza", "success", "restore-test passed", "")
	writeData(w, http.StatusOK, res)
}

// buildRecoveryTarget converts the optional request target into a
// *backup.RecoveryTarget. A nil request yields a nil target (recover to latest).
// A non-empty time is parsed as RFC3339 into a *time.Time.
func buildRecoveryTarget(req *restoreTargetRequest) (*backup.RecoveryTarget, error) {
	if req == nil {
		return nil, nil
	}
	target := &backup.RecoveryTarget{
		XID:    req.XID,
		LSN:    req.LSN,
		Name:   req.Name,
		Action: req.Action,
	}
	if req.Time != "" {
		t, err := time.Parse(time.RFC3339, req.Time)
		if err != nil {
			return nil, core.ValidationError("invalid target time %q (want RFC3339)", req.Time)
		}
		target.Time = &t
	}
	return target, nil
}

// mapBackupRecords maps store records to the wire shape, normalizing nil to [].
func mapBackupRecords(in []store.BackupRecord) []backupRecordResponse {
	out := make([]backupRecordResponse, 0, len(in))
	for _, b := range in {
		out = append(out, backupRecordResponse{
			ID:            b.ID,
			Label:         b.Label,
			BackupType:    b.BackupType,
			StartedAt:     b.StartedAt,
			StoppedAt:     b.StoppedAt,
			SizeBytes:     b.SizeBytes,
			DatabaseBytes: b.DatabaseBytes,
			RepoBytes:     b.RepoBytes,
			WALStart:      b.WALStart,
			WALStop:       b.WALStop,
			Result:        b.Result,
			RepoPath:      b.RepoPath,
			Error:         b.Error,
		})
	}
	return out
}

// mapRestoreTestRecords maps store records to the wire shape, normalizing nil to [].
func mapRestoreTestRecords(in []store.RestoreTestRecord) []restoreTestRecordResponse {
	out := make([]restoreTestRecordResponse, 0, len(in))
	for _, t := range in {
		out = append(out, restoreTestRecordResponse{
			ID:           t.ID,
			TestedAt:     t.TestedAt,
			SourceLabel:  t.SourceLabel,
			VerifiedRows: t.VerifiedRows,
			Result:       t.Result,
			DurationMS:   t.DurationMS,
			Detail:       t.Detail,
		})
	}
	return out
}
