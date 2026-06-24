package server

import (
	"net/http"

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
