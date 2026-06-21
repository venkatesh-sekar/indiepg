package server

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/pgpanel/internal/config"
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

	t.Run("direct TLS is always secure", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "https://x/", nil)
		r.TLS = &tls.ConnectionState{}
		require.True(t, isSecureRequest(r))
	})

	t.Run("spoofed forwarded proto is ignored without trust flag", func(t *testing.T) {
		// Default (no PGPANEL_TRUST_FORWARDED_PROTO): the header is spoofable
		// and must NOT be honored, so the cookie stays non-Secure on plain HTTP.
		t.Setenv(trustForwardedProtoEnv, "")
		r := httptest.NewRequest(http.MethodGet, "http://x/", nil)
		r.Header.Set("X-Forwarded-Proto", "https")
		require.False(t, isSecureRequest(r))
	})

	t.Run("forwarded proto honored only when trust flag set", func(t *testing.T) {
		t.Setenv(trustForwardedProtoEnv, "true")
		r := httptest.NewRequest(http.MethodGet, "http://x/", nil)
		r.Header.Set("X-Forwarded-Proto", "https")
		require.True(t, isSecureRequest(r))
	})

	t.Run("trust flag set but proto http stays insecure", func(t *testing.T) {
		t.Setenv(trustForwardedProtoEnv, "1")
		r := httptest.NewRequest(http.MethodGet, "http://x/", nil)
		r.Header.Set("X-Forwarded-Proto", "http")
		require.False(t, isSecureRequest(r))
	})
}

func TestTrustForwardedProto(t *testing.T) {
	t.Run("unset is false", func(t *testing.T) {
		t.Setenv(trustForwardedProtoEnv, "")
		require.False(t, trustForwardedProto())
	})
	for _, v := range []string{"1", "true", "TRUE", "yes", "on"} {
		t.Run("truthy "+v, func(t *testing.T) {
			t.Setenv(trustForwardedProtoEnv, v)
			require.True(t, trustForwardedProto())
		})
	}
	for _, v := range []string{"", "0", "false", "no", "off", "https"} {
		t.Run("falsy "+v, func(t *testing.T) {
			t.Setenv(trustForwardedProtoEnv, v)
			require.False(t, trustForwardedProto())
		})
	}
}

func TestTokenWithSource(t *testing.T) {
	t.Run("cookie", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "ck"})
		tok, src := tokenWithSource(r)
		require.Equal(t, "ck", tok)
		require.Equal(t, tokenSourceCookie, src)
	})
	t.Run("bearer", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Authorization", "Bearer bt")
		tok, src := tokenWithSource(r)
		require.Equal(t, "bt", tok)
		require.Equal(t, tokenSourceBearer, src)
	})
	t.Run("none", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		tok, src := tokenWithSource(r)
		require.Equal(t, "", tok)
		require.Equal(t, tokenSourceNone, src)
	})
	t.Run("cookie wins over bearer", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "ck"})
		r.Header.Set("Authorization", "Bearer bt")
		_, src := tokenWithSource(r)
		require.Equal(t, tokenSourceCookie, src)
	})
}

func TestIsUnsafeMethod(t *testing.T) {
	unsafe := []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}
	for _, m := range unsafe {
		require.True(t, isUnsafeMethod(m), m)
	}
	safe := []string{http.MethodGet, http.MethodHead, http.MethodOptions}
	for _, m := range safe {
		require.False(t, isUnsafeMethod(m), m)
	}
}

func TestCSRFOriginOK(t *testing.T) {
	srv := &Server{cfg: config.Config{BindAddr: "127.0.0.1:8443"}}

	newReq := func() *http.Request {
		return httptest.NewRequest(http.MethodPost, "/api/config", nil)
	}

	t.Run("matching Origin host accepted", func(t *testing.T) {
		r := newReq()
		r.Header.Set("Origin", "https://127.0.0.1:8443")
		require.True(t, srv.csrfOriginOK(r))
	})

	t.Run("matching Origin host with different port accepted", func(t *testing.T) {
		// A TLS-terminating proxy may rewrite the port; host match suffices.
		r := newReq()
		r.Header.Set("Origin", "https://127.0.0.1")
		require.True(t, srv.csrfOriginOK(r))
	})

	t.Run("matching Referer accepted when no Origin", func(t *testing.T) {
		r := newReq()
		r.Header.Set("Referer", "https://127.0.0.1:8443/app/config")
		require.True(t, srv.csrfOriginOK(r))
	})

	t.Run("foreign Origin rejected", func(t *testing.T) {
		r := newReq()
		r.Header.Set("Origin", "https://evil.example.com")
		require.False(t, srv.csrfOriginOK(r))
	})

	t.Run("foreign Referer rejected", func(t *testing.T) {
		r := newReq()
		r.Header.Set("Referer", "https://evil.example.com/x")
		require.False(t, srv.csrfOriginOK(r))
	})

	t.Run("no Origin or Referer rejected", func(t *testing.T) {
		require.False(t, srv.csrfOriginOK(newReq()))
	})

	t.Run("custom CSRF header accepted regardless of origin", func(t *testing.T) {
		r := newReq()
		r.Header.Set(csrfHeaderName, "1")
		require.True(t, srv.csrfOriginOK(r))
	})

	t.Run("custom header accepted even with foreign Origin", func(t *testing.T) {
		// The non-simple header cannot be set cross-site without a preflight,
		// so its presence is an independent same-origin proof.
		r := newReq()
		r.Header.Set("Origin", "https://evil.example.com")
		r.Header.Set(csrfHeaderName, "1")
		require.True(t, srv.csrfOriginOK(r))
	})

	t.Run("garbage Origin value rejected", func(t *testing.T) {
		r := newReq()
		r.Header.Set("Origin", "://::not a url")
		require.False(t, srv.csrfOriginOK(r))
	})
}

// TestRequireAuthCSRF exercises the CSRF backstop end to end through the
// requireAuth middleware: cookie-authenticated unsafe methods need a same-origin
// signal, while Bearer clients and safe methods are unaffected.
func TestRequireAuthCSRF(t *testing.T) {
	srv, _ := newTestServer(t)
	token := login(t, srv, testPassword)
	bindOrigin := "https://" + config.DefaultBindAddr

	// requireAuth alone — VerifyToken happens after the CSRF gate, so a valid
	// token reaching the protected handler proves the gate passed.
	protected := srv.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	do := func(setup func(*http.Request)) int {
		r := httptest.NewRequest(http.MethodPost, "/api/config", nil)
		setup(r)
		rec := httptest.NewRecorder()
		protected.ServeHTTP(rec, r)
		return rec.Code
	}

	t.Run("cookie POST without origin blocked", func(t *testing.T) {
		code := do(func(r *http.Request) {
			r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
		})
		require.Equal(t, http.StatusConflict, code)
	})

	t.Run("cookie POST with same-origin allowed", func(t *testing.T) {
		code := do(func(r *http.Request) {
			r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
			r.Header.Set("Origin", bindOrigin)
		})
		require.Equal(t, http.StatusOK, code)
	})

	t.Run("cookie POST with foreign origin blocked", func(t *testing.T) {
		code := do(func(r *http.Request) {
			r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
			r.Header.Set("Origin", "https://attacker.example")
		})
		require.Equal(t, http.StatusConflict, code)
	})

	t.Run("cookie POST with custom header allowed", func(t *testing.T) {
		code := do(func(r *http.Request) {
			r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
			r.Header.Set(csrfHeaderName, "1")
		})
		require.Equal(t, http.StatusOK, code)
	})

	t.Run("bearer POST without origin allowed (CSRF-immune)", func(t *testing.T) {
		code := do(func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer "+token)
		})
		require.Equal(t, http.StatusOK, code)
	})

	t.Run("cookie GET without origin allowed (safe method)", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/api/version", nil)
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
		rec := httptest.NewRecorder()
		protected.ServeHTTP(rec, r)
		require.Equal(t, http.StatusOK, rec.Code)
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
