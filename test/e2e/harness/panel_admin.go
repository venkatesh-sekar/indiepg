//go:build e2e

package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// panel_admin.go adds a longer-timeout request seam for the GUIDED-ACTION admin
// writes (create database / new-app / create role / create read-only user).
//
// Why this exists: the frozen Panel.Do uses a 60s http.Client timeout, which is
// right for ordinary endpoints. But CREATE DATABASE copies template1 through WAL,
// and on this deliberately I/O-slow box that single statement can take tens of
// seconds — and under the suite's parallel load (several scenarios doing heavy
// disk work at once) it can exceed 60s, tripping the client timeout even though
// the server completes the work correctly. These guided actions are inherently
// long-running DDL, so they get a generous bounded ceiling instead.
//
// This is additive (a new file with new methods on the frozen *Panel type, in
// the same package); the frozen panel.go is untouched. It reuses the same auth
// (bearer token) and CSRF posture as Panel.Do.

// adminWriteTimeout bounds a single guided-action admin write. It is generous
// (the box is intentionally I/O-slow) but still finite, so a genuinely wedged
// request fails the test rather than hanging the suite.
const adminWriteTimeout = 5 * time.Minute

// doLong issues a request exactly like Panel.Do but through a client whose
// timeout is adminWriteTimeout rather than the frozen 60s, so a slow CREATE
// DATABASE under parallel load does not trip the client deadline.
func (p *Panel) doLong(method, path string, body any) (*Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), adminWriteTimeout)
	defer cancel()

	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request body: %w", err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.BaseURL+path, rdr)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if p.token != "" {
		req.Header.Set("Authorization", "Bearer "+p.token)
	}
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		req.Header.Set("X-Indiepg-Csrf", "1")
	}

	client := &http.Client{Timeout: adminWriteTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return &Response{Status: resp.StatusCode, Body: data}, nil
}

// PostLong is POST with the generous admin-write timeout, decoding the success
// envelope into out (out may be nil) and returning a typed *PanelError on a
// non-2xx — the same contract as Panel.POST, just without the 60s client cap.
func (p *Panel) PostLong(path string, body, out any) error {
	resp, err := p.doLong(http.MethodPost, path, body)
	if err != nil {
		return err
	}
	return resp.DecodeData(out)
}
