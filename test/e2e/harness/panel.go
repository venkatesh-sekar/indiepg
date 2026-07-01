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

// Panel is a typed HTTP client for the panel API. It speaks to the published host
// port over plain HTTP (the panel binds 0.0.0.0:8443 inside the container via the
// e2e systemd drop-in). After Login it attaches Authorization: Bearer on every
// request and X-Indiepg-Csrf: 1 on unsafe methods, exactly as a real SPA client.
//
// Scenario authors add new typed methods by calling the exported Do/GET/POST/PUT/
// DELETE helpers; they need not modify this type.
type Panel struct {
	BaseURL string
	token   string
	http    *http.Client
}

func newPanel(baseURL string) *Panel {
	return &Panel{
		BaseURL: baseURL,
		http:    &http.Client{Timeout: 60 * time.Second},
	}
}

// Token returns the current session bearer token (empty before Login).
func (p *Panel) Token() string { return p.token }

// Response is the result of a panel request: the HTTP status and raw body, plus
// helpers to decode the success envelope or extract a typed error.
type Response struct {
	Status int
	Body   []byte
}

// apiError mirrors the panel's JSON error envelope (internal/server/respond.go).
type apiError struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Hint    string         `json:"hint,omitempty"`
	Details map[string]any `json:"details,omitempty"`
}

// Err returns a non-nil error when the status is >= 400, carrying the panel's
// stable code + message so scenarios can assert on the failure kind.
func (r *Response) Err() error {
	if r.Status < 400 {
		return nil
	}
	var ae apiError
	_ = json.Unmarshal(r.Body, &ae)
	if ae.Code != "" || ae.Message != "" {
		return &PanelError{Status: r.Status, Code: ae.Code, Message: ae.Message, Hint: ae.Hint, Details: ae.Details}
	}
	return &PanelError{Status: r.Status, Message: string(r.Body)}
}

// DecodeData unmarshals the {"data": ...} success envelope into out.
func (r *Response) DecodeData(out any) error {
	if err := r.Err(); err != nil {
		return err
	}
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(r.Body, &env); err != nil {
		return fmt.Errorf("decode envelope (status %d): %w; body=%s", r.Status, err, string(r.Body))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(env.Data, out)
}

// PanelError is a typed API failure (status + stable code).
type PanelError struct {
	Status  int
	Code    string
	Message string
	Hint    string
	Details map[string]any
}

func (e *PanelError) Error() string {
	return fmt.Sprintf("panel API error: status=%d code=%s message=%q hint=%q", e.Status, e.Code, e.Message, e.Hint)
}

// Do issues a request to the panel and returns the raw Response (it does NOT fail
// on a 4xx/5xx — use Response.Err / DecodeData). body is JSON-encoded when non-nil.
// It is the low-level seam new typed methods build on.
func (p *Panel) Do(method, path string, body any) (*Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
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
		// Bearer clients are CSRF-immune, but the panel still wants the header on
		// unsafe methods; setting it always is harmless and matches the SPA.
		req.Header.Set("X-Indiepg-Csrf", "1")
	}
	resp, err := p.http.Do(req)
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

// GET/POST/PUT/DELETE are thin helpers over Do that decode the success envelope
// into out (out may be nil) and return a typed error on a non-2xx.
func (p *Panel) GET(path string, out any) error    { return p.do2(http.MethodGet, path, nil, out) }
func (p *Panel) DELETE(path string, out any) error { return p.do2(http.MethodDelete, path, nil, out) }
func (p *Panel) POST(path string, body, out any) error {
	return p.do2(http.MethodPost, path, body, out)
}
func (p *Panel) PUT(path string, body, out any) error { return p.do2(http.MethodPut, path, body, out) }

func (p *Panel) do2(method, path string, body, out any) error {
	resp, err := p.Do(method, path, body)
	if err != nil {
		return err
	}
	return resp.DecodeData(out)
}

// ---- Public (unauthenticated) endpoints ----

// Readyz returns nil when GET /readyz answers 200.
func (p *Panel) Readyz() error { return p.probe("/readyz") }

// Healthz returns nil when GET /healthz answers 200.
func (p *Panel) Healthz() error { return p.probe("/healthz") }

func (p *Panel) probe(path string) error {
	resp, err := p.Do(http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	if resp.Status != http.StatusOK {
		return fmt.Errorf("%s returned %d", path, resp.Status)
	}
	return nil
}

// ---- Auth ----

type loginResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Login authenticates with the admin password and stores the returned bearer
// token for subsequent requests.
func (p *Panel) Login(password string) error {
	var lr loginResponse
	if err := p.POST("/api/auth/login", map[string]string{"password": password}, &lr); err != nil {
		return err
	}
	if lr.Token == "" {
		return fmt.Errorf("login succeeded but returned an empty token")
	}
	p.token = lr.Token
	return nil
}

// ---- Read endpoints ----

// Instance is the GET /api/instance payload.
type Instance struct {
	InstanceID   string    `json:"instance_id"`
	Label        string    `json:"label"`
	Hostname     string    `json:"hostname"`
	PGSystemID   string    `json:"pg_system_id"`
	PanelVersion string    `json:"panel_version"`
	CreatedAt    time.Time `json:"created_at"`
}

// Instance returns the panel's instance identity (a trivial authenticated GET).
func (p *Panel) Instance() (Instance, error) {
	var out Instance
	err := p.GET("/api/instance", &out)
	return out, err
}

// Health is the GET /api/health payload.
type Health struct {
	Panel     string    `json:"panel"`
	Store     string    `json:"store"`
	CheckedAt time.Time `json:"checked_at"`
}

// Health returns the panel's self-health snapshot.
func (p *Panel) Health() (Health, error) {
	var out Health
	err := p.GET("/api/health", &out)
	return out, err
}
