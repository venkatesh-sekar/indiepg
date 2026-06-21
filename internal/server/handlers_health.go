package server

import (
	"context"
	"net/http"
	"time"

	"github.com/venkatesh-sekar/indiepg/internal/core"
)

// handleLiveness is the unauthenticated liveness probe. It returns 200 as long
// as the process can serve requests; it does not touch dependencies.
func (s *Server) handleLiveness(w http.ResponseWriter, _ *http.Request) {
	writeData(w, http.StatusOK, map[string]any{"status": "ok"})
}

// readinessResponse reports dependency health for the unauthenticated readiness
// probe. The store must be reachable; the panel deliberately keeps working even
// when the managed Postgres is down, so PG is not part of readiness.
type readinessResponse struct {
	Status string `json:"status"`
	Store  string `json:"store"`
}

// handleReadiness checks the local store is reachable. A failing store is a
// 503 with a JSON body so an orchestrator can distinguish it from liveness.
func (s *Server) handleReadiness(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	resp := readinessResponse{Status: "ok", Store: "ok"}
	if err := s.store.Ping(ctx); err != nil {
		resp.Status = "unavailable"
		resp.Store = "down"
		writeData(w, http.StatusServiceUnavailable, resp)
		return
	}
	writeData(w, http.StatusOK, resp)
}

// versionResponse carries the build version for the authenticated UI footer.
type versionResponse struct {
	Version string `json:"version"`
}

// handleVersion returns the build version (set via -ldflags).
func (s *Server) handleVersion(w http.ResponseWriter, _ *http.Request) {
	v := core.Version
	if v == "" {
		v = "dev"
	}
	writeData(w, http.StatusOK, versionResponse{Version: v})
}

// handleInstance returns the panel's stable identity (instance id, label,
// hostname, pg system id) for the dashboard header. Secrets are never included.
func (s *Server) handleInstance(w http.ResponseWriter, r *http.Request) {
	inst, err := s.store.GetInstance(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, inst)
}

// healthResponse is a single green/red summary for the "is my DB OK" badge.
// PG health is reported as a string the SPA can render without panicking when
// the managed Postgres is unreachable.
type healthResponse struct {
	Panel     string    `json:"panel"`
	Store     string    `json:"store"`
	CheckedAt time.Time `json:"checked_at"`
}

// handleHealth is the authenticated composite health endpoint.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	resp := healthResponse{Panel: "ok", Store: "ok", CheckedAt: time.Now().UTC()}
	if err := s.store.Ping(ctx); err != nil {
		resp.Store = "down"
	}
	writeData(w, http.StatusOK, resp)
}

// audit records a panel action. It best-effort writes to the store and never
// fails the request on an audit error (a failed audit is logged, not surfaced).
func (s *Server) audit(ctx context.Context, action, target, result, summary string, code core.Code) {
	detail := ""
	if code != "" {
		detail = string(code)
	}
	entry := storeAuditEntry(action, target, result, summary, detail)
	if _, err := s.store.AppendAudit(ctx, entry); err != nil {
		s.log.Warn("audit append failed", "action", action, "err", err)
	}
}
