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

// Restore plumbing owned by the restore-after-loss scenario (scenario 4). It is
// ADDITIVE: a new file in package harness with UNIQUELY-named types/method, so it
// never collides with the PITR author's panel_pitr.go (which independently
// declares RestoreTarget/RestoreResult/RestoreToTarget). It does not modify the
// frozen core.
//
// The restore endpoint is SYNCHRONOUS — handleRestore takes a full pre-restore
// safety backup and then runs `pgbackrest restore` inline, only responding when
// the whole operation finishes. That can exceed the frozen Panel.Do/POST 60s
// client cap, so this method issues the POST on its own long-lived client/context
// rather than the 60s seam.

// RestoreSpec is the optional point-in-time-recovery target for RunRestore. It
// mirrors internal/server restoreTargetRequest: exactly one of Time/XID/LSN/Name
// selects where recovery stops; all empty means "recover to the latest available
// WAL". Action is the post-recovery action (promote|pause|shutdown); empty leaves
// pgBackRest's default.
type RestoreSpec struct {
	Time   string `json:"time,omitempty"`
	XID    string `json:"xid,omitempty"`
	LSN    string `json:"lsn,omitempty"`
	Name   string `json:"name,omitempty"`
	Action string `json:"action,omitempty"`
}

// restoreReqBody mirrors the panel's POST /api/backups/restore request body.
// Confirm must equal the stanza name to clear the manager's typed-name guard.
type restoreReqBody struct {
	Target  *RestoreSpec `json:"target,omitempty"`
	Delta   bool         `json:"delta"`
	Confirm string       `json:"confirm"`
}

// RestoreOutcome is the decoded core.Result the restore endpoint returns once the
// (safety-backup-then-)restore has run to completion. Data carries "stanza",
// "delta", "safety_backup_label" and (for a targeted restore) "pitr".
type RestoreOutcome struct {
	OK      bool           `json:"ok"`
	Message string         `json:"message"`
	Data    map[string]any `json:"data"`
}

// SafetyLabel returns the pgBackRest label of the safety backup the manager took
// before overwriting the live cluster (empty if absent).
func (r RestoreOutcome) SafetyLabel() string {
	if r.Data == nil {
		return ""
	}
	s, _ := r.Data["safety_backup_label"].(string)
	return s
}

// RunRestore drives a guarded restore via POST /api/backups/restore. Pass
// spec=nil to recover to the latest WAL. confirm must equal the stanza name. It
// blocks until the synchronous safety-backup + restore finishes (or fails with a
// typed error), on a 15-minute client so a slow restore is not truncated by the
// frozen 60s POST cap.
func (p *Panel) RunRestore(spec *RestoreSpec, delta bool, confirm string) (RestoreOutcome, error) {
	raw, err := json.Marshal(restoreReqBody{Target: spec, Delta: delta, Confirm: confirm})
	if err != nil {
		return RestoreOutcome{}, err
	}

	const timeout = 15 * time.Minute // safety full backup + pgBackRest restore worst case.
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.BaseURL+"/api/backups/restore", bytes.NewReader(raw))
	if err != nil {
		return RestoreOutcome{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if p.token != "" {
		req.Header.Set("Authorization", "Bearer "+p.token)
	}
	req.Header.Set("X-Indiepg-Csrf", "1") // matches the SPA on unsafe methods.

	client := &http.Client{Timeout: timeout}
	httpResp, err := client.Do(req)
	if err != nil {
		return RestoreOutcome{}, err
	}
	defer func() { _ = httpResp.Body.Close() }()

	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return RestoreOutcome{}, err
	}

	// Reuse the frozen Response decode/error envelope so error codes surface the
	// same way as every other typed method.
	resp := &Response{Status: httpResp.StatusCode, Body: body}
	var out RestoreOutcome
	if err := resp.DecodeData(&out); err != nil {
		return RestoreOutcome{}, err
	}
	return out, nil
}
