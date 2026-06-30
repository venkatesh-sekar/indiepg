//go:build e2e

package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"
)

// This file adds the typed Panel client surface the PG version-upgrade scenario
// drives (minor, major, finalize, rollback). It is additive — a new file the
// scenario author owns — and does not touch the frozen harness core. Every method
// is a thin wrapper over Panel.Do/GET/POST, except MajorPreflight, which is a
// SYNCHRONOUS long-running endpoint (it apt-installs the target packages inline)
// and therefore needs a request timeout larger than the frozen client's 60s cap;
// it uses a dedicated long-timeout request built from the panel's exported BaseURL
// + bearer token rather than mutating the shared client.

// ---- GET /api/pg/version ----

// PGCurrentVersion is the running cluster's version (mirrors pgCurrentVersion).
type PGCurrentVersion struct {
	Full  string `json:"full"`
	Major int    `json:"major"`
}

// PGMinorUpdate reports whether a newer minor of the running major is available.
type PGMinorUpdate struct {
	Available bool   `json:"available"`
	Target    string `json:"target"`
}

// PGMajorOption is one offered major-upgrade target.
type PGMajorOption struct {
	Major   int  `json:"major"`
	Default bool `json:"default"`
}

// PGAvailableUpdates is the minor + major upgrade offer set.
type PGAvailableUpdates struct {
	Minor  PGMinorUpdate   `json:"minor"`
	Majors []PGMajorOption `json:"majors"`
}

// PendingFinalization mirrors pg.PendingFinalization: the durable record of a
// completed major upgrade still keeping the old cluster as a rollback point.
type PendingFinalization struct {
	FromMajor        int       `json:"from_major"`
	ToMajor          int       `json:"to_major"`
	ReclaimableBytes int64     `json:"reclaimable_bytes"`
	UpgradedAt       time.Time `json:"upgraded_at"`
}

// PGVersionInfo is the GET /api/pg/version payload.
type PGVersionInfo struct {
	Running             bool                 `json:"running"`
	Current             PGCurrentVersion     `json:"current"`
	Available           PGAvailableUpdates   `json:"available"`
	PendingFinalization *PendingFinalization `json:"pending_finalization"`
}

// PGVersion fetches the running version, available updates and any pending
// finalization (GET /api/pg/version).
func (p *Panel) PGVersion() (PGVersionInfo, error) {
	var out PGVersionInfo
	err := p.GET("/api/pg/version", &out)
	return out, err
}

// ---- upgrade operation status ----

// UpgradeOperation mirrors pg.OperationState: the status of the current/last
// upgrade operation the SPA polls.
type UpgradeOperation struct {
	Kind        string     `json:"kind"`
	Status      string     `json:"status"`
	Phase       string     `json:"phase"`
	Message     string     `json:"message"`
	Error       string     `json:"error"`
	FromMajor   int        `json:"from_major"`
	TargetMajor int        `json:"target_major"`
	Log         []string   `json:"log"`
	StartedAt   time.Time  `json:"started_at"`
	FinishedAt  *time.Time `json:"finished_at"`
}

// UpgradeStatusDoc is the GET /api/pg/upgrade/status payload (and the 202 ack of
// every async upgrade op).
type UpgradeStatusDoc struct {
	Operation *UpgradeOperation    `json:"operation"`
	Pending   *PendingFinalization `json:"pending_finalization"`
}

// GetUpgradeStatus reads the current operation + pending state (GET
// /api/pg/upgrade/status).
func (p *Panel) GetUpgradeStatus() (UpgradeStatusDoc, error) {
	var out UpgradeStatusDoc
	err := p.GET("/api/pg/upgrade/status", &out)
	return out, err
}

// ---- POST /api/pg/upgrade/minor ----

// MinorUpgrade starts the async minor upgrade. On the no-update path the panel
// refuses with a typed 409 conflict (surfaced as a *PanelError here); on success
// it returns the 202 status document to poll via AwaitUpgradeOp.
func (p *Panel) MinorUpgrade(backup bool) (UpgradeStatusDoc, error) {
	var out UpgradeStatusDoc
	err := p.POST("/api/pg/upgrade/minor", map[string]bool{"backup": backup}, &out)
	return out, err
}

// ---- POST /api/pg/upgrade/major/preflight ----

// PreflightCheck is one named pre-flight result (mirrors pg.Check). Status is
// "pass" | "warn" | "fail".
type PreflightCheck struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Status      string `json:"status"`
	Message     string `json:"message"`
	Remediation string `json:"remediation"`
}

// PreflightPreview is the wizard preview block.
type PreflightPreview struct {
	FromMajor         int      `json:"from_major"`
	ToMajor           int      `json:"to_major"`
	DiskRequiredBytes int64    `json:"disk_required_bytes"`
	DiskFreeBytes     int64    `json:"disk_free_bytes"`
	Extensions        []string `json:"extensions"`
	Blocking          bool     `json:"blocking"`
}

// PreflightResult is the inline {checks, preview} contract of the major preflight.
type PreflightResult struct {
	Checks  []PreflightCheck `json:"checks"`
	Preview PreflightPreview `json:"preview"`
}

// HasFail reports whether any check is a hard blocker (status "fail").
func (r PreflightResult) HasFail() bool {
	for _, c := range r.Checks {
		if c.Status == "fail" {
			return true
		}
	}
	return false
}

// Check returns the check with the given id (and whether it was found).
func (r PreflightResult) Check(id string) (PreflightCheck, bool) {
	for _, c := range r.Checks {
		if c.ID == id {
			return c, true
		}
	}
	return PreflightCheck{}, false
}

// MajorPreflight runs §5 Phase A synchronously: it apt-installs the target-major
// packages and runs the upgrade checklist, returning {checks, preview}. The
// inline install can outlast the frozen client's 60s timeout on a loaded box, so
// it issues a dedicated long-timeout request.
func (p *Panel) MajorPreflight(target int, timeout time.Duration) (PreflightResult, error) {
	var out PreflightResult
	err := p.longPOST("/api/pg/upgrade/major/preflight", map[string]int{"target_major": target}, &out, timeout)
	return out, err
}

// ---- POST /api/pg/upgrade/major/start ----

// MajorStart begins §5 Phase B asynchronously (mandatory backup → pg_upgradecluster
// → reconfigure → analyze → smoke test → pending finalization). Poll with
// AwaitUpgradeOp(kind="major").
func (p *Panel) MajorStart(target int, confirm bool) (UpgradeStatusDoc, error) {
	var out UpgradeStatusDoc
	err := p.POST("/api/pg/upgrade/major/start", map[string]any{"target_major": target, "confirm": confirm}, &out)
	return out, err
}

// ---- POST /api/pg/upgrade/finalize ----

// FinalizeUpgrade drops the old cluster (point of no return). confirmVersion must
// equal the OLD major. Poll with AwaitUpgradeOp(kind="finalize").
func (p *Panel) FinalizeUpgrade(confirmVersion int) (UpgradeStatusDoc, error) {
	var out UpgradeStatusDoc
	err := p.POST("/api/pg/upgrade/finalize", map[string]int{"confirm_version": confirmVersion}, &out)
	return out, err
}

// ---- POST /api/pg/upgrade/rollback ----

// RollbackUpgrade returns the box to the old major before finalize. confirmVersion
// must equal the LIVE (new) major whose post-upgrade writes are discarded. Poll
// with AwaitUpgradeOp(kind="rollback").
func (p *Panel) RollbackUpgrade(confirmVersion int) (UpgradeStatusDoc, error) {
	var out UpgradeStatusDoc
	err := p.POST("/api/pg/upgrade/rollback", map[string]int{"confirm_version": confirmVersion}, &out)
	return out, err
}

// ---- polling ----

// AwaitUpgradeOp polls the upgrade status until the current operation matches the
// expected kind AND reaches a terminal state (success/failed), then returns it.
// Matching on kind guards against reading a previous operation's terminal record.
func (p *Panel) AwaitUpgradeOp(t *testing.T, kind string, timeout time.Duration) UpgradeOperation {
	t.Helper()
	var final UpgradeOperation
	Await(t, timeout, 2*time.Second, "upgrade operation "+kind+" to finish", func() (bool, error) {
		st, err := p.GetUpgradeStatus()
		if err != nil {
			return false, err
		}
		if st.Operation == nil || st.Operation.Kind != kind {
			return false, fmt.Errorf("operation is not yet kind=%s", kind)
		}
		switch st.Operation.Status {
		case "success", "failed":
			final = *st.Operation
			return true, nil
		}
		return false, nil
	})
	return final
}

// longPOST issues an authenticated JSON POST with a caller-chosen client timeout,
// for synchronous endpoints that can outlast the frozen client's fixed timeout. It
// reuses the panel's exported BaseURL + bearer token and decodes the standard
// success envelope; it is purely additive and does not touch the frozen core.
func (p *Panel) longPOST(path string, body, out any, timeout time.Duration) error {
	b, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal request body: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.BaseURL+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Indiepg-Csrf", "1")
	if tok := p.Token(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	r := &Response{Status: resp.StatusCode, Body: data}
	return r.DecodeData(out)
}
