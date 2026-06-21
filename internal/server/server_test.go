package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/indiepg/internal/auth"
	"github.com/venkatesh-sekar/indiepg/internal/config"
	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/store"
)

const testPassword = "correct-horse-battery-staple"

// newTestServer wires a Server over an in-memory store with the admin password
// already set, and a small in-memory SPA. It returns the server and the store
// so tests can manipulate state directly.
func newTestServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()

	st, err := store.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	ctx := context.Background()
	// Install an admin password so login works.
	authn := auth.New(st, auth.DefaultLockoutPolicy(), defaultSessionTTL)
	require.NoError(t, authn.SetPassword(ctx, testPassword))

	cfg := config.Default()
	srv, err := newServer(cfg, st, core.Discard(), authn, testDist(), defaultSessionTTL)
	require.NoError(t, err)
	return srv, st
}

// login performs a login against the server and returns the session token.
func login(t *testing.T, srv *Server, password string) string {
	t.Helper()
	body, _ := json.Marshal(loginRequest{Password: password})
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(body))
	srv.Handler().ServeHTTP(rec, r)
	require.Equal(t, http.StatusOK, rec.Code, "login body: %s", rec.Body.String())

	var env struct {
		Data loginResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	require.NotEmpty(t, env.Data.Token)
	return env.Data.Token
}

func TestLivenessIsPublic(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestReadinessReportsStore(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	var env struct {
		Data readinessResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	require.Equal(t, "ok", env.Data.Store)
}

func TestLoginSucceedsAndSetsCookie(t *testing.T) {
	srv, _ := newTestServer(t)
	body, _ := json.Marshal(loginRequest{Password: testPassword})
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(body))
	srv.Handler().ServeHTTP(rec, r)

	require.Equal(t, http.StatusOK, rec.Code)
	var found bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName {
			found = true
			require.True(t, c.HttpOnly)
			require.Equal(t, http.SameSiteStrictMode, c.SameSite)
			require.NotEmpty(t, c.Value)
		}
	}
	require.True(t, found, "session cookie must be set")
}

func TestLoginRejectsBadPassword(t *testing.T) {
	srv, _ := newTestServer(t)
	body, _ := json.Marshal(loginRequest{Password: "wrong"})
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(body))
	srv.Handler().ServeHTTP(rec, r)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	var ae apiError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ae))
	require.Equal(t, core.CodeAuth, ae.Code)
}

func TestLoginRejectsMissingPassword(t *testing.T) {
	srv, _ := newTestServer(t)
	body, _ := json.Marshal(loginRequest{})
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(body))
	srv.Handler().ServeHTTP(rec, r)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestProtectedEndpointRequiresAuth(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/instance", nil))

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	var ae apiError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ae))
	require.Equal(t, core.CodeAuth, ae.Code)
}

func TestProtectedEndpointWithBearerToken(t *testing.T) {
	srv, _ := newTestServer(t)
	token := login(t, srv, testPassword)

	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/version", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	srv.Handler().ServeHTTP(rec, r)

	require.Equal(t, http.StatusOK, rec.Code)
	var env struct {
		Data versionResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	require.NotEmpty(t, env.Data.Version)
}

func TestProtectedEndpointWithCookie(t *testing.T) {
	srv, _ := newTestServer(t)
	token := login(t, srv, testPassword)

	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/version", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	srv.Handler().ServeHTTP(rec, r)
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestTamperedTokenRejected(t *testing.T) {
	srv, _ := newTestServer(t)
	token := login(t, srv, testPassword)

	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/version", nil)
	r.Header.Set("Authorization", "Bearer "+token+"tampered")
	srv.Handler().ServeHTTP(rec, r)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestAuthStatusReportsInstalledAndAuthenticated(t *testing.T) {
	srv, _ := newTestServer(t)

	// Unauthenticated status: installed (password set) but not authenticated.
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/auth/status", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	var env struct {
		Data authStatusResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	require.True(t, env.Data.Installed)
	require.False(t, env.Data.Authenticated)

	// Authenticated status.
	token := login(t, srv, testPassword)
	rec = httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/auth/status", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	srv.Handler().ServeHTTP(rec, r)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	require.True(t, env.Data.Authenticated)
}

func TestWhoamiReturnsSessionSubject(t *testing.T) {
	srv, _ := newTestServer(t)
	token := login(t, srv, testPassword)

	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/auth/whoami", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	srv.Handler().ServeHTTP(rec, r)

	require.Equal(t, http.StatusOK, rec.Code)
	var env struct {
		Data whoamiResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	require.False(t, env.Data.ExpiresAt.IsZero())
	require.True(t, env.Data.ExpiresAt.After(env.Data.IssuedAt) || env.Data.ExpiresAt.Equal(env.Data.IssuedAt))
}

func TestWhoamiRequiresAuth(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/auth/whoami", nil))
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestLogoutClearsCookie(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName {
			require.True(t, c.MaxAge < 0, "logout must expire the cookie")
		}
	}
}

func TestSPAFallbackThroughRouter(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/some/spa/route", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "id=root")
}

func TestInstanceEndpointReturnsIdentity(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := context.Background()
	require.NoError(t, st.SaveInstance(ctx, store.Instance{
		InstanceID:   "inst-123",
		Label:        "web-db-01",
		Hostname:     "host-1",
		PanelVersion: "test",
		CreatedAt:    time.Now().UTC(),
	}))

	token := login(t, srv, testPassword)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/instance", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	srv.Handler().ServeHTTP(rec, r)

	require.Equal(t, http.StatusOK, rec.Code)
	var env struct {
		Data store.Instance `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	require.Equal(t, "inst-123", env.Data.InstanceID)
	require.Equal(t, "web-db-01", env.Data.Label)
}

func TestAuditEndpointListsLoginEvents(t *testing.T) {
	srv, _ := newTestServer(t)
	token := login(t, srv, testPassword) // produces a "login success" audit row

	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/audit?limit=10", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	srv.Handler().ServeHTTP(rec, r)

	require.Equal(t, http.StatusOK, rec.Code)
	var env struct {
		Data struct {
			Entries []store.AuditEntry `json:"entries"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	require.NotEmpty(t, env.Data.Entries)
	require.Equal(t, "login", env.Data.Entries[0].Action)
}

func TestConfigGetAndUpdateRoundTrip(t *testing.T) {
	srv, _ := newTestServer(t)
	token := login(t, srv, testPassword)

	// Update retention + bucket.
	upd := updateConfigRequest{
		RetentionDays: intp(30),
		Backup:        &backupTargetUpdate{Bucket: strp("rt-bucket")},
	}
	body, _ := json.Marshal(upd)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/api/config", bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+token)
	srv.Handler().ServeHTTP(rec, r)
	require.Equal(t, http.StatusOK, rec.Code, "update body: %s", rec.Body.String())

	// Read it back.
	rec = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodGet, "/api/config", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	srv.Handler().ServeHTTP(rec, r)
	require.Equal(t, http.StatusOK, rec.Code)

	var env struct {
		Data struct {
			Config            config.Config `json:"config"`
			BackupSecretIsSet bool          `json:"backup_secret_is_set"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	require.Equal(t, 30, env.Data.Config.RetentionDays)
	require.Equal(t, "rt-bucket", env.Data.Config.Backup.Bucket)
}

func TestConfigSecretNeverSerialized(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := context.Background()
	// Persist a secret directly.
	cfg := config.Default()
	cfg.Backup.SecretKey = "top-secret"
	require.NoError(t, config.Save(ctx, st, cfg))

	token := login(t, srv, testPassword)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	srv.Handler().ServeHTTP(rec, r)
	require.Equal(t, http.StatusOK, rec.Code)

	require.NotContains(t, rec.Body.String(), "top-secret")
	require.Contains(t, rec.Body.String(), `"backup_secret_is_set":true`)
}

func TestSecurityHeadersPresent(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	require.Equal(t, "DENY", rec.Header().Get("X-Frame-Options"))
	require.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	require.NotEmpty(t, rec.Header().Get("Content-Security-Policy"))
}

func TestNewRequiresStore(t *testing.T) {
	_, err := New(Options{Store: nil})
	require.Error(t, err)
	require.Equal(t, core.CodeInternal, core.CodeOf(err))
}
