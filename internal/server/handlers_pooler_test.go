package server

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/indiepg/internal/pgbouncer"
)

// decodePoolerStatus pulls the poolerStatus out of the standard {"data": ...}
// envelope the API wraps every success in.
func decodePoolerStatus(t *testing.T, body []byte) poolerStatus {
	t.Helper()
	var env struct {
		Data poolerStatus `json:"data"`
	}
	require.NoError(t, json.Unmarshal(body, &env))
	return env.Data
}

// The pooler ships OFF: with nothing persisted, the status reports disabled and
// the loopback address apps would use once it is on. Pool sizing is nil because
// the test server's Postgres is never connected (max_connections unknown) — the
// surface degrades to "computed at enable time" rather than guessing.
func TestPoolerStatus_DefaultOff(t *testing.T) {
	srv, _ := newTestServer(t)
	token := login(t, srv, testPassword)

	rec := authedRequest(t, srv, http.MethodGet, "/api/pooler", token, nil)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	got := decodePoolerStatus(t, rec.Body.Bytes())
	require.False(t, got.Enabled, "pooler must default to OFF")
	require.Equal(t, pgbouncer.LoopbackHost, got.Host)
	require.Equal(t, pgbouncer.DefaultListenPort, got.ListenPort)
	require.Nil(t, got.Pool, "sizing must be nil when Postgres is unreachable, not a guess")
}

// Once the enable flag is persisted, the status reflects it. This proves the
// handler reads the same key the enable orchestrator writes (default-off comes
// from the key being absent, not from a hardcoded false).
func TestPoolerStatus_ReflectsPersistedEnable(t *testing.T) {
	srv, st := newTestServer(t)
	token := login(t, srv, testPassword)

	// Flip it on through the same state interface Enable persists to.
	require.NoError(t, st.SetConfig(context.Background(), pgbouncer.EnabledConfigKey, "true"))

	rec := authedRequest(t, srv, http.MethodGet, "/api/pooler", token, nil)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	got := decodePoolerStatus(t, rec.Body.Bytes())
	require.True(t, got.Enabled, "status must reflect the persisted enable flag")
}

// The status endpoint is behind requireAuth: an unauthenticated request is
// rejected (401), never served the SPA fallback (which would mean it leaks
// pooler state to anyone).
func TestPoolerStatus_RequiresAuth(t *testing.T) {
	srv, _ := newTestServer(t)

	rec := authedRequest(t, srv, http.MethodGet, "/api/pooler", "not-a-valid-token", nil)
	require.Equal(t, http.StatusUnauthorized, rec.Code, "body: %s", rec.Body.String())
}

// Enabling is system-mutating (installs + starts a service), so the endpoint is
// behind requireAuth: an unauthenticated POST is rejected before any side effect.
func TestPoolerEnable_RequiresAuth(t *testing.T) {
	srv, st := newTestServer(t)

	rec := authedRequest(t, srv, http.MethodPost, "/api/pooler/enable", "not-a-valid-token",
		map[string]any{"roles": []string{"app"}})
	require.Equal(t, http.StatusUnauthorized, rec.Code, "body: %s", rec.Body.String())

	// And nothing was persisted: the pooler stays OFF.
	enabled, err := pgbouncer.IsEnabled(context.Background(), st)
	require.NoError(t, err)
	require.False(t, enabled, "a rejected request must not enable the pooler")
}

// Bad input is rejected fast, before any IO: an empty role list would render an
// auth_file that locks every app out of the pooler, so it is a 400 and the
// pooler is never touched. This guard runs before the Postgres/install path, so
// the assertion is deterministic even though the test server's PG is unreachable.
func TestPoolerEnable_EmptyRolesRejected(t *testing.T) {
	srv, st := newTestServer(t)
	token := login(t, srv, testPassword)

	rec := authedRequest(t, srv, http.MethodPost, "/api/pooler/enable", token,
		map[string]any{"roles": []string{}})
	require.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())

	enabled, err := pgbouncer.IsEnabled(context.Background(), st)
	require.NoError(t, err)
	require.False(t, enabled, "a rejected request must not enable the pooler")
}

// A typo'd workload profile is a 400 (ParseWorkloadProfile refuses to silently
// mis-size the pool), again before any install/PG side effect.
func TestPoolerEnable_UnknownProfileRejected(t *testing.T) {
	srv, _ := newTestServer(t)
	token := login(t, srv, testPassword)

	rec := authedRequest(t, srv, http.MethodPost, "/api/pooler/enable", token,
		map[string]any{"roles": []string{"app"}, "profile": "nonsense"})
	require.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
}

// With valid input but Postgres unreachable (the test server never connects), the
// pool cannot be sized from a live max_connections. Rather than guess and then
// install a service against a fabricated size, the handler refuses with a clear
// conflict and leaves the pooler OFF — proving the size-from-live-PG guard fires
// before any system mutation.
func TestPoolerEnable_RefusesWhenPostgresUnreachable(t *testing.T) {
	srv, st := newTestServer(t)
	token := login(t, srv, testPassword)

	rec := authedRequest(t, srv, http.MethodPost, "/api/pooler/enable", token,
		map[string]any{"roles": []string{"app"}, "profile": "oltp"})
	require.Equal(t, http.StatusConflict, rec.Code, "body: %s", rec.Body.String())

	enabled, err := pgbouncer.IsEnabled(context.Background(), st)
	require.NoError(t, err)
	require.False(t, enabled, "the pooler must stay OFF when it could not be brought up")
}
