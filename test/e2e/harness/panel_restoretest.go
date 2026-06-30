//go:build e2e

package harness

import (
	"context"
	"io"
	"net/http"
	"time"
)

// RestoreTestResult is the decoded core.Result envelope POST /api/backups/restore-test
// returns. The deep path records Data["method"]="scratch restore + boot" and a real
// Data["verified_rows"]; the WithData(...) map is carried verbatim, so its JSON
// numbers decode to float64 (read them through the accessors below).
type RestoreTestResult struct {
	OK      bool           `json:"ok"`
	Message string         `json:"message"`
	Data    map[string]any `json:"data"`
}

// Method returns the Data["method"] datum ("scratch restore + boot" for the deep
// drill, "" if absent).
func (r RestoreTestResult) Method() string {
	if v, ok := r.Data["method"].(string); ok {
		return v
	}
	return ""
}

// VerifiedRows returns the Data["verified_rows"] datum as an int64 (0 if absent).
func (r RestoreTestResult) VerifiedRows() int64 { return r.intData("verified_rows") }

// HistoryID returns the Data["history_id"] datum — the store.restore_tests row id
// the drill recorded — as an int64 (0 if absent).
func (r RestoreTestResult) HistoryID() int64 { return r.intData("history_id") }

func (r RestoreTestResult) intData(key string) int64 {
	switch v := r.Data[key].(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	}
	return 0
}

// RestoreTest drives POST /api/backups/restore-test. deep=true selects the heavy,
// non-destructive scratch-restore+boot durability drill via ?deep=true (which the
// handler maps to Manager.RestoreTestDeep); deep=false runs the cheap read-only
// `pgbackrest verify`. The decoded core.Result is returned for assertion.
//
// The endpoint is SYNCHRONOUS: handleRestoreTest runs the whole drill inline on the
// request's context and only responds once it finishes (restore -> boot with full
// WAL replay -> row count -> scratch teardown), so the request must stay open for
// the entire run. The frozen Panel.Do/POST client caps every request at 60s — well
// under the deep drill's worst case — and the handler does NOT detach the context,
// so a client timeout would cancel the request context and kill pgbackrest
// mid-restore, recording a false "fail". This method therefore issues the POST on
// its own long-lived client/context instead of the frozen 60s seam. It is purely
// additive (new file in package harness) and does not modify the frozen core.
func (p *Panel) RestoreTest(deep bool) (RestoreTestResult, error) {
	path := "/api/backups/restore-test"
	if deep {
		path += "?deep=true"
	}

	const timeout = 15 * time.Minute // > Manager deepBootTimeout (10m) + restore/teardown.
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.BaseURL+path, nil)
	if err != nil {
		return RestoreTestResult{}, err
	}
	if p.token != "" {
		req.Header.Set("Authorization", "Bearer "+p.token)
	}
	req.Header.Set("X-Indiepg-Csrf", "1") // matches the SPA on unsafe methods.

	client := &http.Client{Timeout: timeout}
	httpResp, err := client.Do(req)
	if err != nil {
		return RestoreTestResult{}, err
	}
	defer func() { _ = httpResp.Body.Close() }()

	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return RestoreTestResult{}, err
	}

	// Reuse the frozen Response decode/error envelope so error codes surface the
	// same way as every other typed method.
	resp := &Response{Status: httpResp.StatusCode, Body: body}
	var out RestoreTestResult
	if err := resp.DecodeData(&out); err != nil {
		return RestoreTestResult{}, err
	}
	return out, nil
}
