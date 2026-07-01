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

// Typed Panel methods for the opt-in PgBouncer pooler API (GET /api/pooler,
// POST /api/pooler/enable, POST /api/pooler/disable). This file is ADDITIVE: it
// adds new uniquely-named types/methods in package harness and only builds on the
// exported Do/GET/Response seam in panel.go — it does not touch the frozen core.
//
// Enable and disable do NOT go through the frozen Panel.POST seam: enabling the
// pooler apt-installs the pgbouncer package (apt-get update + install) and then
// brings the unit up, which can exceed the frozen client's 60s cap on an I/O-slow
// box. So both drive the request on a dedicated, long-lived client/context
// (mirroring panel_restore.go's RunRestore) while reusing the frozen Response
// envelope decoder, so error codes surface identically to every other method.

// PoolerPool mirrors the host-sized pool sizing block the pooler status and
// enable payloads carry (internal/pgbouncer.PoolRecommendation).
type PoolerPool struct {
	Profile           string `json:"profile"`
	PgMaxConnections  int    `json:"pg_max_connections"`
	DefaultPoolSize   int    `json:"default_pool_size"`
	MinPoolSize       int    `json:"min_pool_size"`
	ReservePoolSize   int    `json:"reserve_pool_size"`
	MaxClientConn     int    `json:"max_client_conn"`
	ServerIdleTimeout int    `json:"server_idle_timeout"`
}

// PoolerStatus mirrors GET /api/pooler (server.poolerStatus): the on/off flag,
// the loopback address apps use to reach the pooler, and the sizing enabling
// would apply (nil when Postgres is unreachable).
type PoolerStatus struct {
	Enabled    bool        `json:"enabled"`
	Host       string      `json:"host"`
	ListenPort int         `json:"listen_port"`
	Pool       *PoolerPool `json:"pool"`
}

// PoolerEnableResult mirrors pgbouncer.EnableResult: what the idempotent enable
// flow did, including the authoritative Running flag (the handler only returns
// success once it has confirmed the unit is up).
type PoolerEnableResult struct {
	PooledRoles     []string   `json:"pooled_roles"`
	Pool            PoolerPool `json:"pool"`
	ConfigChanged   bool       `json:"config_changed"`
	UserlistChanged bool       `json:"userlist_changed"`
	Reloaded        bool       `json:"reloaded"`
	Running         bool       `json:"running"`
}

// GetPooler fetches the read-only pooler status (GET /api/pooler).
func (p *Panel) GetPooler() (PoolerStatus, error) {
	var out PoolerStatus
	err := p.GET("/api/pooler", &out)
	return out, err
}

// poolerEnableBody mirrors server.poolerEnableRequest. max_connections is
// deliberately not sent — the panel sizes the pool server-side from the live
// Postgres.
type poolerEnableBody struct {
	Roles   []string `json:"roles"`
	Profile string   `json:"profile,omitempty"`
}

// EnablePooler turns the pooler on (POST /api/pooler/enable) for the given login
// roles and workload profile (empty = the mixed default). It runs on a long-lived
// client because the enable flow apt-installs pgbouncer before starting it.
func (p *Panel) EnablePooler(roles []string, profile string) (PoolerEnableResult, error) {
	var out PoolerEnableResult
	err := p.postLong("/api/pooler/enable",
		poolerEnableBody{Roles: roles, Profile: profile}, 12*time.Minute, &out)
	return out, err
}

// DisablePooler turns the pooler back off (POST /api/pooler/disable). It takes no
// body and returns the now-off status in the same shape as GetPooler.
func (p *Panel) DisablePooler() (PoolerStatus, error) {
	var out PoolerStatus
	err := p.postLong("/api/pooler/disable", nil, 5*time.Minute, &out)
	return out, err
}

// postLong issues a POST on a dedicated client/context (the frozen Do caps at
// 60s; the pooler enable's apt-install needs longer), then reuses the frozen
// Response envelope decoder so a non-2xx surfaces as the same typed *PanelError.
func (p *Panel) postLong(path string, body any, timeout time.Duration, out any) error {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(raw)
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.BaseURL+path, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if p.token != "" {
		req.Header.Set("Authorization", "Bearer "+p.token)
	}
	req.Header.Set("X-Indiepg-Csrf", "1") // matches the SPA on unsafe methods.

	client := &http.Client{Timeout: timeout}
	httpResp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = httpResp.Body.Close() }()

	raw, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return err
	}
	return (&Response{Status: httpResp.StatusCode, Body: raw}).DecodeData(out)
}
