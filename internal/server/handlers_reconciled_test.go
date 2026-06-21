package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/indiepg/internal/core"
)

// authedRequest issues an authenticated request (Bearer token, so the CSRF
// backstop is skipped) through the real router and returns the recorder.
func authedRequest(t *testing.T, srv *Server, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		rdr = bytes.NewReader(raw)
	} else {
		rdr = bytes.NewReader(nil)
	}
	r := httptest.NewRequest(method, path, rdr)
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	r.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, r)
	return rec
}

// TestReconciledRoutesRequireAuth verifies every newly wired route is behind
// requireAuth: without a session they return 401, never the SPA fallback (which
// would mean the route is unregistered).
func TestReconciledRoutesRequireAuth(t *testing.T) {
	srv, _ := newTestServer(t)

	cases := []struct {
		method, path string
	}{
		{http.MethodGet, "/api/dashboard"},
		{http.MethodPost, "/api/query"},
		{http.MethodGet, "/api/databases"},
		{http.MethodPost, "/api/databases"},
		{http.MethodPost, "/api/databases/new-app"},
		{http.MethodDelete, "/api/databases/foo"},
		{http.MethodGet, "/api/roles"},
		{http.MethodPost, "/api/roles"},
		{http.MethodPost, "/api/roles/readonly"},
		{http.MethodPost, "/api/roles/foo/rotate"},
		{http.MethodDelete, "/api/roles/foo"},
		{http.MethodPost, "/api/grants"},
		{http.MethodDelete, "/api/grants"},
		{http.MethodGet, "/api/backups"},
		{http.MethodPost, "/api/backups/run"},
		{http.MethodPost, "/api/backups/restore"},
		{http.MethodPost, "/api/backups/restore-test"},
		{http.MethodGet, "/api/alerts"},
		{http.MethodPut, "/api/alerts/rules"},
		{http.MethodDelete, "/api/alerts/rules/abc"},
		{http.MethodPut, "/api/alerts/channels"},
		{http.MethodPost, "/api/alerts/channels/test"},
		{http.MethodPost, "/api/migrate/sessions"},
		{http.MethodGet, "/api/migrate/sessions/abc"},
		{http.MethodDelete, "/api/migrate/sessions/abc"},
		{http.MethodPost, "/api/migrate/single-db"},
		{http.MethodPost, "/api/migrate/cluster"},
	}
	for _, c := range cases {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			r := httptest.NewRequest(c.method, c.path, bytes.NewReader([]byte("{}")))
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, r)
			require.Equal(t, http.StatusUnauthorized, rec.Code,
				"route should be registered behind requireAuth (401), got %d", rec.Code)
		})
	}
}

// TestMigrateEndpointsTypedUnavailable verifies the migration endpoints exist
// (no 404) and return an honest typed error rather than pretending to work.
func TestMigrateEndpointsTypedUnavailable(t *testing.T) {
	srv, _ := newTestServer(t)
	token := login(t, srv, testPassword)

	for _, path := range []string{"/api/migrate/single-db", "/api/migrate/cluster", "/api/migrate/sessions"} {
		rec := authedRequest(t, srv, http.MethodPost, path, token, map[string]any{})
		require.Equal(t, http.StatusInternalServerError, rec.Code)
		var ae apiError
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ae))
		require.Equal(t, core.CodeInternal, ae.Code)
		require.Contains(t, strings.ToLower(ae.Message), "not available")
	}
}

// TestAlertsRuleRoundTrip exercises the alerts rule CRUD path end-to-end through
// the real in-memory store (no Postgres needed): save a rule, then read it back.
func TestAlertsRuleRoundTrip(t *testing.T) {
	srv, _ := newTestServer(t)
	token := login(t, srv, testPassword)

	rule := map[string]any{
		"id":               "test-cpu",
		"name":             "High CPU",
		"metric":           "host.cpu.percent",
		"op":               ">",
		"threshold":        90,
		"severity":         "warning",
		"for_seconds":      60,
		"cooldown_seconds": 300,
		"enabled":          true,
	}
	rec := authedRequest(t, srv, http.MethodPut, "/api/alerts/rules", token, rule)
	require.Equal(t, http.StatusOK, rec.Code, "save rule body: %s", rec.Body.String())

	rec = authedRequest(t, srv, http.MethodGet, "/api/alerts", token, nil)
	require.Equal(t, http.StatusOK, rec.Code)

	var env struct {
		Data struct {
			Channels []map[string]any `json:"channels"`
			Rules    []map[string]any `json:"rules"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	require.NotNil(t, env.Data.Channels, "channels must serialize as [] not null")
	require.Len(t, env.Data.Rules, 1)
	require.Equal(t, "test-cpu", env.Data.Rules[0]["id"])
	require.EqualValues(t, 60, env.Data.Rules[0]["for_seconds"])
}

// TestBackupsHistoryEmpty verifies the history endpoint returns empty (non-null)
// arrays through the real store and the {data} envelope.
func TestBackupsHistoryEmpty(t *testing.T) {
	srv, _ := newTestServer(t)
	token := login(t, srv, testPassword)

	rec := authedRequest(t, srv, http.MethodGet, "/api/backups", token, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, `"backups":[]`, "nil slices must serialize as []")
	require.Contains(t, body, `"restore_tests":[]`)
	require.True(t, strings.HasPrefix(strings.TrimSpace(body), `{"data":`), "success must be wrapped in the data envelope")
}

// TestQueryDegradesWhenPGDown verifies the query route is wired and degrades to
// a typed error (not a 404 or panic) when Postgres is not connected — which is
// the test server's state (newServer never calls Connect).
func TestQueryDegradesWhenPGDown(t *testing.T) {
	srv, _ := newTestServer(t)
	token := login(t, srv, testPassword)

	rec := authedRequest(t, srv, http.MethodPost, "/api/query", token, map[string]any{"sql": "SELECT 1"})
	require.Equal(t, http.StatusInternalServerError, rec.Code,
		"query with no PG connection should be a typed 500, got %d: %s", rec.Code, rec.Body.String())
	var ae apiError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ae))
	require.Equal(t, core.CodeInternal, ae.Code)
}

// TestDropDatabaseConfirmationEmitsExpected verifies a typed-name confirmation
// mismatch returns a CodeSafety error whose `expected` field carries the value
// the operator must type (the SPA reads this to render its confirm dialog). This
// path runs entirely before any Postgres pool use (confirmation is checked in
// the pure builder), so it is testable without a live database.
func TestDropDatabaseConfirmationEmitsExpected(t *testing.T) {
	srv, _ := newTestServer(t)
	token := login(t, srv, testPassword)

	rec := authedRequest(t, srv, http.MethodDelete, "/api/databases/orders", token,
		map[string]any{"confirm": "wrong"})
	require.Equal(t, http.StatusConflict, rec.Code, "body: %s", rec.Body.String())

	var ae apiError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ae))
	require.Equal(t, core.CodeSafety, ae.Code)
	require.Equal(t, "orders", ae.Expected, "the confirm value must be surfaced as `expected`")
}

// TestQueryRejectsWrites verifies the guard blocks a non-read statement before
// any execution, with a CodeSafety error.
func TestQueryRejectsWrites(t *testing.T) {
	srv, _ := newTestServer(t)
	token := login(t, srv, testPassword)

	rec := authedRequest(t, srv, http.MethodPost, "/api/query", token, map[string]any{"sql": "DELETE FROM users"})
	require.Equal(t, http.StatusConflict, rec.Code, "a write must be rejected by the guard (409 safety)")
	var ae apiError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ae))
	require.Equal(t, core.CodeSafety, ae.Code)
}
