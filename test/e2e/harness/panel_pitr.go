//go:build e2e

package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"
)

// PITR restore plumbing, owned by the point-in-time-recovery scenario. It is an
// ADDITIVE helper file that builds only on the frozen Panel fields (BaseURL,
// token) and adds no methods to the frozen core types. The restore-after-loss
// author keeps their own restore plumbing in panel_restore.go; to avoid colliding
// in package harness, every identifier here is PITR-namespaced.

// PITRTarget is the optional point-in-time-recovery target a scenario hands to
// RestoreToTarget. It mirrors the panel's restoreTargetRequest (internal/server/
// handlers_backups.go): exactly one of Time/XID/LSN/Name selects where recovery
// stops, and Action is the post-recovery action (promote|pause|shutdown). A
// zero-value target means "recover to the latest available WAL".
//
// Determinism note: PITR scenarios use XID (a stable transaction id) rather than
// Time, so there is no wall-clock dependency. pgBackRest recovery is INCLUSIVE by
// default (target-exclusive is off), so the transaction whose id equals XID is
// itself applied before recovery stops — the RecoveryTarget type the product
// exposes (internal/backup/command.go) has no inclusive/exclusive knob, so the
// pgBackRest default governs.
type PITRTarget struct {
	Time   string // RFC3339; leave empty for an xid/lsn/name target
	XID    string // recover to (and including) this transaction id
	LSN    string // recover to this log sequence number
	Name   string // recover to this named restore point
	Action string // promote|pause|shutdown (post-recovery action)
}

// pitrRestoreRequest mirrors internal/server/handlers_backups.go restoreRequest.
// The Target pointer is nil for a latest-WAL restore.
type pitrRestoreRequest struct {
	Target  *pitrRestoreTargetBody `json:"target,omitempty"`
	Delta   bool                   `json:"delta"`
	Confirm string                 `json:"confirm"`
}

type pitrRestoreTargetBody struct {
	Time   string `json:"time,omitempty"`
	XID    string `json:"xid,omitempty"`
	LSN    string `json:"lsn,omitempty"`
	Name   string `json:"name,omitempty"`
	Action string `json:"action,omitempty"`
}

// PITRRestoreResult is the decoded core.Result envelope POST /api/backups/restore
// returns. On success Data carries "stanza", "delta", the "safety_backup_label"
// recovery point, and (for a non-empty target) "pitr": true.
type PITRRestoreResult struct {
	OK      bool           `json:"ok"`
	Message string         `json:"message"`
	Data    map[string]any `json:"data"`
}

// SafetyBackupLabel returns the Data["safety_backup_label"] datum — the pre-restore
// safety snapshot the manager takes before overwriting the live cluster — or "".
func (r PITRRestoreResult) SafetyBackupLabel() string {
	if v, ok := r.Data["safety_backup_label"].(string); ok {
		return v
	}
	return ""
}

// IsPITR reports whether the restore recorded Data["pitr"]=true (a non-empty
// recovery target was applied).
func (r PITRRestoreResult) IsPITR() bool {
	v, _ := r.Data["pitr"].(bool)
	return v
}

// RestoreToTarget drives POST /api/backups/restore: a guarded, destructive restore
// of the live cluster to the given recovery target. confirm must equal the stanza
// name (the typed-name confirmation the manager requires before overwriting the
// live cluster); delta selects an in-place delta restore over the existing data
// directory.
//
// The endpoint is SYNCHRONOUS: handleRestore takes a full pre-restore safety
// backup and then runs `pgbackrest restore` inline, only responding once the whole
// thing finishes. That can far exceed the frozen Panel.Do/POST client's 60s cap,
// so (like panel_restoretest.go) this method issues the POST on its own long-lived
// client/context rather than the frozen 60s seam. It is purely additive (a new file
// in package harness) and does not modify the frozen core.
func (p *Panel) RestoreToTarget(target *PITRTarget, delta bool, confirm string) (PITRRestoreResult, error) {
	body := pitrRestoreRequest{Delta: delta, Confirm: confirm}
	if target != nil {
		body.Target = &pitrRestoreTargetBody{
			Time:   target.Time,
			XID:    target.XID,
			LSN:    target.LSN,
			Name:   target.Name,
			Action: target.Action,
		}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return PITRRestoreResult{}, err
	}

	const timeout = 15 * time.Minute // safety full backup + pgBackRest restore worst case.
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.BaseURL+"/api/backups/restore", bytes.NewReader(raw))
	if err != nil {
		return PITRRestoreResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if p.token != "" {
		req.Header.Set("Authorization", "Bearer "+p.token)
	}
	req.Header.Set("X-Indiepg-Csrf", "1") // matches the SPA on unsafe methods.

	client := &http.Client{Timeout: timeout}
	httpResp, err := client.Do(req)
	if err != nil {
		return PITRRestoreResult{}, err
	}
	defer func() { _ = httpResp.Body.Close() }()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return PITRRestoreResult{}, err
	}

	// Reuse the frozen Response decode/error envelope so error codes surface the
	// same way as every other typed method.
	resp := &Response{Status: httpResp.StatusCode, Body: respBody}
	var out PITRRestoreResult
	if err := resp.DecodeData(&out); err != nil {
		return PITRRestoreResult{}, err
	}
	return out, nil
}
