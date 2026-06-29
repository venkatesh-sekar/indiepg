package server

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/pg"
)

func setPendingUpgrade(t *testing.T, srv *Server) {
	t.Helper()
	_, err := srv.upgrades.Mutate(context.Background(), func(st *pg.UpgradeState) {
		st.Pending = &pg.PendingFinalization{
			FromMajor:  16,
			ToMajor:    17,
			UpgradedAt: time.Now().UTC(),
		}
		st.OldClusterPort = "5433"
	})
	require.NoError(t, err)
}

func TestUpgradeRollbackRequiresTypedLiveVersion(t *testing.T) {
	srv, _ := newTestServer(t)
	setPendingUpgrade(t, srv)
	token := login(t, srv, testPassword)

	rec := authedRequest(t, srv, http.MethodPost, "/api/pg/upgrade/rollback", token,
		map[string]any{"confirm_version": 16})
	require.Equal(t, http.StatusConflict, rec.Code, "body: %s", rec.Body.String())

	var got apiError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, core.CodeSafety, got.Code)
	require.Contains(t, got.RequiredFlags, "confirm_version=17")
}

func TestUpgradeStatusMatchesFrontendEnvelope(t *testing.T) {
	srv, _ := newTestServer(t)
	setPendingUpgrade(t, srv)
	token := login(t, srv, testPassword)

	rec := authedRequest(t, srv, http.MethodGet, "/api/pg/upgrade/status", token, nil)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	var env struct {
		Data upgradeStatusResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	require.Nil(t, env.Data.Operation)
	require.NotNil(t, env.Data.Pending)
	require.Equal(t, 16, env.Data.Pending.FromMajor)
	require.Equal(t, 17, env.Data.Pending.ToMajor)
}
