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

// TestValidateUpgradeTarget locks the sole guard that stops a destructive
// same-major or downgrade "upgrade" (validateUpgradeTarget gates both the
// preflight and the start endpoints, handlers_pgversion.go:240,324). A major
// upgrade runs pg_upgradecluster over the live datadir, so accepting a
// same-major or downgrade target — or an unsupported one for which no target
// binary exists — is a data-loss-class mistake. Each rejecting case pins the
// specific branch so a one-line flip (e.g. `target <= current` → `target <
// current`) reds the test.
func TestValidateUpgradeTarget(t *testing.T) {
	cases := []struct {
		name          string
		current       int
		target        int
		wantErr       bool
		wantCode      core.Code
		wantMsgSubstr string
	}{
		{name: "accept next major", current: 16, target: 17},
		{name: "accept skip a major", current: 15, target: 17},
		{name: "reject downgrade", current: 17, target: 16,
			wantErr: true, wantCode: core.CodeValidation, wantMsgSubstr: "newer"},
		{name: "reject same major", current: 16, target: 16,
			wantErr: true, wantCode: core.CodeValidation, wantMsgSubstr: "newer"},
		{name: "reject unsupported target", current: 16, target: 99,
			wantErr: true, wantCode: core.CodeValidation, wantMsgSubstr: "not a supported"},
		{name: "reject unknown current version", current: 0, target: 17,
			wantErr: true, wantCode: core.CodeInternal, wantMsgSubstr: "current"},
		{name: "reject negative current version", current: -1, target: 17,
			wantErr: true, wantCode: core.CodeInternal, wantMsgSubstr: "current"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateUpgradeTarget(tc.current, tc.target)
			if !tc.wantErr {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.Equal(t, tc.wantCode, core.CodeOf(err))
			require.Contains(t, err.Error(), tc.wantMsgSubstr)
		})
	}
}

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
