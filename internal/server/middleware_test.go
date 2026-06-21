package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTokenFromRequest(t *testing.T) {
	t.Run("from cookie", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "cookie-token"})
		require.Equal(t, "cookie-token", tokenFromRequest(r))
	})

	t.Run("from bearer header", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Authorization", "Bearer hdr-token")
		require.Equal(t, "hdr-token", tokenFromRequest(r))
	})

	t.Run("bearer is case-insensitive on scheme", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Authorization", "bearer lower")
		require.Equal(t, "lower", tokenFromRequest(r))
	})

	t.Run("cookie takes precedence over header", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "cookie"})
		r.Header.Set("Authorization", "Bearer header")
		require.Equal(t, "cookie", tokenFromRequest(r))
	})

	t.Run("none", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		require.Equal(t, "", tokenFromRequest(r))
	})

	t.Run("non-bearer authorization ignored", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Authorization", "Basic abc")
		require.Equal(t, "", tokenFromRequest(r))
	})
}

func TestSetAndClearSessionCookie(t *testing.T) {
	t.Run("set secure", func(t *testing.T) {
		rec := httptest.NewRecorder()
		setSessionCookie(rec, "tok", time.Hour, true)
		c := rec.Result().Cookies()[0]
		require.Equal(t, sessionCookieName, c.Name)
		require.Equal(t, "tok", c.Value)
		require.True(t, c.HttpOnly)
		require.True(t, c.Secure)
		require.Equal(t, http.SameSiteStrictMode, c.SameSite)
		require.Greater(t, c.MaxAge, 0)
	})

	t.Run("set insecure (local http)", func(t *testing.T) {
		rec := httptest.NewRecorder()
		setSessionCookie(rec, "tok", time.Hour, false)
		c := rec.Result().Cookies()[0]
		require.False(t, c.Secure)
	})

	t.Run("clear expires the cookie", func(t *testing.T) {
		rec := httptest.NewRecorder()
		clearSessionCookie(rec, true)
		c := rec.Result().Cookies()[0]
		require.Equal(t, "", c.Value)
		require.Less(t, c.MaxAge, 0)
	})
}

func TestIsSecureRequest(t *testing.T) {
	t.Run("plain http", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "http://x/", nil)
		require.False(t, isSecureRequest(r))
	})

	t.Run("forwarded proto https", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "http://x/", nil)
		r.Header.Set("X-Forwarded-Proto", "https")
		require.True(t, isSecureRequest(r))
	})
}

func TestParseIntQuery(t *testing.T) {
	tests := []struct {
		query string
		key   string
		def   int
		want  int
	}{
		{"limit=50", "limit", 100, 50},
		{"limit=abc", "limit", 100, 100},
		{"", "limit", 100, 100},
		{"offset=-5", "offset", 0, -5},
	}
	for _, tc := range tests {
		t.Run(tc.query, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/?"+tc.query, nil)
			require.Equal(t, tc.want, parseIntQuery(r, tc.key, tc.def))
		})
	}
}

func TestStoreAuditEntry(t *testing.T) {
	e := storeAuditEntry("login", "auth", "success", "ok", "detail")
	require.Equal(t, "login", e.Action)
	require.Equal(t, "auth", e.Target)
	require.Equal(t, "success", e.Result)
	require.Equal(t, "ok", e.Summary)
	require.Equal(t, "detail", e.Detail)
	require.Equal(t, auditActor, e.Actor)
	require.False(t, e.TS.IsZero())
}
