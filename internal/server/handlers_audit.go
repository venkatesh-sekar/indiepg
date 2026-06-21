package server

import (
	"net/http"
	"strconv"
	"time"

	"github.com/venkatesh-sekar/indiepg/internal/store"
)

// auditActor is the recorded actor for panel-originated actions. There is a
// single admin in v1, so a constant suffices.
const auditActor = "admin"

// storeAuditEntry builds a store.AuditEntry with the current UTC timestamp.
func storeAuditEntry(action, target, result, summary, detail string) store.AuditEntry {
	return store.AuditEntry{
		TS:      time.Now().UTC(),
		Actor:   auditActor,
		Action:  action,
		Target:  target,
		Summary: summary,
		Result:  result,
		Detail:  detail,
	}
}

// handleListAudit returns recent audit entries (newest first) with limit/offset
// query parameters. It is the in-panel "audit log of every action" view.
func (s *Server) handleListAudit(w http.ResponseWriter, r *http.Request) {
	limit := parseIntQuery(r, "limit", 100)
	offset := parseIntQuery(r, "offset", 0)
	if limit < 1 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	if offset < 0 {
		offset = 0
	}

	entries, err := s.store.ListAudit(r.Context(), limit, offset)
	if err != nil {
		writeError(w, err)
		return
	}
	if entries == nil {
		entries = []store.AuditEntry{}
	}
	writeData(w, http.StatusOK, map[string]any{
		"entries": entries,
		"limit":   limit,
		"offset":  offset,
	})
}

// parseIntQuery reads an integer query parameter, returning def when absent or
// malformed.
func parseIntQuery(r *http.Request, key string, def int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}
