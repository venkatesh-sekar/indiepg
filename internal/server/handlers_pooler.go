package server

import (
	"net/http"

	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/pg"
	"github.com/venkatesh-sekar/indiepg/internal/pgbouncer"
)

// poolerStatus is the read-only view the Settings page renders for the opt-in
// PgBouncer pooler: whether it is on, where same-box apps reach it (loopback
// only), and — when Postgres is reachable — the host-sized pool sizing that
// enabling would apply, so the UI can label each setting by its effect before
// the operator turns the pooler on.
//
// This surface NEVER mutates anything (no package install, no service touch);
// it only reads the persisted enable flag and computes the recommended sizing.
// Actually enabling the pooler is a separate, explicitly-confirmed action.
type poolerStatus struct {
	// Enabled is the operator's persisted decision; false (the default-off state)
	// until the pooler is explicitly enabled.
	Enabled bool `json:"enabled"`
	// Host and ListenPort tell apps where the pooler accepts connections. The host
	// is always loopback — the pooler is never bound to a public interface.
	Host       string `json:"host"`
	ListenPort int    `json:"listen_port"`
	// Pool is the host-sized sizing (for the Mixed best-default profile) that
	// enabling would apply, so the UI can show what each setting does. It is nil
	// when Postgres is unreachable (max_connections unknown): rather than guess,
	// we report no sizing and the UI explains it is computed at enable time.
	Pool *pgbouncer.PoolRecommendation `json:"pool"`
}

// handleGetPoolerStatus returns the read-only PgBouncer pooler status: the on/off
// state, the loopback address apps use to reach it, and the host-sized pool
// sizing enabling would apply (best-effort — nil when Postgres is unreachable).
func (s *Server) handleGetPoolerStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	enabled, err := pgbouncer.IsEnabled(ctx, s.store)
	if err != nil {
		writeError(w, err)
		return
	}

	status := poolerStatus{
		Enabled:    enabled,
		Host:       pgbouncer.LoopbackHost,
		ListenPort: pgbouncer.DefaultListenPort,
	}

	// Host-sized pool sizing needs Postgres' max_connections. CurrentTuning is
	// best-effort: if Postgres is unreachable it returns Applied == nil (and a nil
	// error), in which case we leave Pool nil rather than size against a guess —
	// the UI then says the sizing is computed when the pooler is enabled. A read
	// of pg_settings can never disrupt the pooler's reported state, so a tuning
	// error is non-fatal here.
	if tuning, terr := s.pg.CurrentTuning(ctx); terr == nil && tuning.Applied != nil {
		pool := pgbouncer.RecommendPool(tuning.Applied.MaxConnections, pg.ProfileMixed)
		status.Pool = &pool
	}

	writeData(w, http.StatusOK, status)
}

// poolerEnableRequest is the operator's opt-in input to turn the pooler on.
// max_connections is deliberately NOT accepted from the client — the pool is
// sized server-side against the live Postgres so a forged value can't widen the
// pool past what the database can serve.
type poolerEnableRequest struct {
	// Roles are the login roles whose traffic is routed through the pooler. At
	// least one is required: an empty auth_file would lock every app out.
	Roles []string `json:"roles"`
	// Profile selects the pool sizing (oltp/olap/mixed); empty = the mixed default.
	Profile string `json:"profile"`
}

// handlePoolerEnable turns the opt-in PgBouncer pooler on. This is a deliberate,
// system-mutating action (it installs the pgbouncer package and starts the
// service), so it is a CSRF-gated POST behind requireAuth. The pool is sized from
// the live Postgres max_connections, never the request, and the enable flag is
// only persisted once the service is confirmed up (see pgbouncer.Manager.Enable).
func (s *Server) handlePoolerEnable(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req poolerEnableRequest
	if err := decodeJSON(r, &req, maxBodyBytes); err != nil {
		writeError(w, err)
		return
	}

	// Validate input before any IO so a bad request fails fast and clearly, rather
	// than surfacing as a downstream "Postgres unreachable" or install error.
	if len(req.Roles) == 0 {
		writeError(w, core.ValidationError("name at least one login role to route through the pooler").
			WithHint("an empty pooler auth_file would lock every app out"))
		return
	}
	profile, err := pg.ParseWorkloadProfile(req.Profile)
	if err != nil {
		writeError(w, err)
		return
	}

	// Size the pool against the live max_connections rather than trusting the
	// client. CurrentTuning is best-effort: if Postgres is unreachable, Applied is
	// nil and we cannot size the pool — refuse with a clear error rather than guess,
	// since enabling installs and starts a service against that sizing.
	tuning, err := s.pg.CurrentTuning(ctx)
	if err != nil {
		writeError(w, err)
		return
	}
	if tuning.Applied == nil {
		writeError(w, core.ConflictError("cannot enable the pooler while Postgres is unreachable").
			WithHint("the pool is sized from Postgres' max_connections; start Postgres and try again"))
		return
	}

	result, err := s.pooler.Enable(ctx, s.pg, s.store, pgbouncer.EnableParams{
		RoleNames:        req.Roles,
		PGMaxConnections: tuning.Applied.MaxConnections,
		Profile:          profile,
	})
	if err != nil {
		s.audit(ctx, "enable_pooler", "pgbouncer", "failure", "enable PgBouncer pooler failed", core.CodeOf(err))
		writeError(w, err)
		return
	}

	s.audit(ctx, "enable_pooler", "pgbouncer", "success", "PgBouncer pooler enabled", "")
	writeData(w, http.StatusOK, result)
}
