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

// TestLoginLockoutThrottlesAfterMaxAttempts proves the brute-force lockout is
// wired end-to-end through the HTTP login handler — not just the authenticator.
// After the policy's max consecutive failures the handler stops returning 401
// and starts returning 429 CodeLocked, and from then on even the *correct*
// password is throttled, so an attacker who finally guesses it still cannot get
// in until the lock expires. The public auth-status endpoint surfaces the state.
func TestLoginLockoutThrottlesAfterMaxAttempts(t *testing.T) {
	const maxAttempts = 3

	st, err := store.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	policy := auth.LockoutPolicy{MaxAttempts: maxAttempts, Window: 15 * time.Minute, LockFor: 15 * time.Minute}
	authn := auth.New(st, policy, defaultSessionTTL)
	require.NoError(t, authn.SetPassword(context.Background(), testPassword))

	srv, err := newServer(config.Default(), st, core.Discard(), authn, testDist(), defaultSessionTTL)
	require.NoError(t, err)

	postLogin := func(password string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(loginRequest{Password: password})
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(body))
		srv.Handler().ServeHTTP(rec, r)
		return rec
	}

	// Captured before any request so the lock deadline (now+LockFor) is provably
	// after it by a 15-minute margin — a flake-proof "deadline is in the future"
	// check that never races wall-clock.
	beforeAttempts := time.Now()

	// The first N-1 wrong guesses are plain credential failures (401 CodeAuth).
	for i := 1; i < maxAttempts; i++ {
		rec := postLogin("wrong")
		require.Equal(t, http.StatusUnauthorized, rec.Code, "attempt %d should be a plain auth failure", i)
		var ae apiError
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ae))
		require.Equal(t, core.CodeAuth, ae.Code)
	}

	// The Nth wrong guess trips the lockout: 429 CodeLocked.
	rec := postLogin("wrong")
	require.Equal(t, http.StatusTooManyRequests, rec.Code, "the Nth failure must trip the lockout")
	var ae apiError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ae))
	require.Equal(t, core.CodeLocked, ae.Code)

	// Crucially, the correct password is now ALSO throttled — the lockout is not
	// bypassable by finally guessing right.
	rec = postLogin(testPassword)
	require.Equal(t, http.StatusTooManyRequests, rec.Code, "correct password must stay locked out")
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ae))
	require.Equal(t, core.CodeLocked, ae.Code)

	// The public auth-status endpoint surfaces the locked state with a deadline,
	// so the SPA can tell the operator to wait rather than show a generic error.
	statusRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(statusRec, httptest.NewRequest(http.MethodGet, "/api/auth/status", nil))
	require.Equal(t, http.StatusOK, statusRec.Code)
	var env struct {
		Data authStatusResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(statusRec.Body.Bytes(), &env))
	require.True(t, env.Data.Locked, "auth status must report locked")
	require.NotNil(t, env.Data.LockedUntil)
	require.True(t, env.Data.LockedUntil.After(beforeAttempts), "lock deadline must be in the future")
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

func TestLogoutInvalidatesSessionServerSide(t *testing.T) {
	srv, _ := newTestServer(t)

	// tokenLives reports whether token still authenticates a protected request.
	tokenLives := func(token string) bool {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/api/version", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		srv.Handler().ServeHTTP(rec, r)
		return rec.Code == http.StatusOK
	}
	doLogout := func(setup func(*http.Request)) int {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
		setup(r)
		srv.Handler().ServeHTTP(rec, r)
		return rec.Code
	}

	token := login(t, srv, testPassword)
	require.True(t, tokenLives(token))

	// An authenticated logout (cookie flow with the SPA's CSRF header) rotates
	// the signing secret, so the token a client still holds is dead server-side.
	require.Equal(t, http.StatusOK, doLogout(func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
		r.Header.Set(csrfHeaderName, "1")
	}))
	require.False(t, tokenLives(token), "token must be invalid after authenticated logout")
}

func TestLogoutWithBearerInvalidatesSession(t *testing.T) {
	srv, _ := newTestServer(t)
	token := login(t, srv, testPassword)

	// A Bearer logout is CSRF-immune and skips the origin check, so a valid
	// Bearer token alone proves the live session and rotates the secret.
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	srv.Handler().ServeHTTP(rec, r)
	require.Equal(t, http.StatusOK, rec.Code)

	// The token is dead server-side after the Bearer-authenticated logout.
	rec = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodGet, "/api/version", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	srv.Handler().ServeHTTP(rec, r)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestLogoutWithoutProofDoesNotInvalidate(t *testing.T) {
	srv, _ := newTestServer(t)

	tokenLives := func(token string) bool {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/api/version", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		srv.Handler().ServeHTTP(rec, r)
		return rec.Code == http.StatusOK
	}
	doLogout := func(setup func(*http.Request)) int {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
		setup(r)
		srv.Handler().ServeHTTP(rec, r)
		return rec.Code
	}

	token := login(t, srv, testPassword)

	// An anonymous logout clears any cookie but must not rotate the secret —
	// otherwise the public endpoint is an unauthenticated force-logout (DoS).
	require.Equal(t, http.StatusOK, doLogout(func(*http.Request) {}))
	require.True(t, tokenLives(token), "anonymous logout must not kill live sessions")

	// A cookie logout lacking a same-origin/CSRF signal also must not rotate, so
	// a cross-site request cannot force the admin to re-authenticate.
	require.Equal(t, http.StatusOK, doLogout(func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	}))
	require.True(t, tokenLives(token), "cookie logout without CSRF signal must not rotate")
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
