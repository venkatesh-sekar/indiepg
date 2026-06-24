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
