package server

import (
	"context"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/venkatesh-sekar/pgpanel/internal/auth"
	"github.com/venkatesh-sekar/pgpanel/internal/core"
)

// sessionCookieName is the name of the signed session cookie. It is host-only,
// HttpOnly, SameSite=Strict, and Secure-aware so a stolen cookie cannot be read
// by JS or sent cross-site.
const sessionCookieName = "pgpanel_session"

// ctxKey is an unexported context key type to avoid collisions.
type ctxKey int

const (
	ctxKeySession ctxKey = iota
)

// sessionFromContext returns the authenticated session attached by requireAuth,
// or nil if the request is unauthenticated.
func sessionFromContext(ctx context.Context) *auth.Session {
	s, _ := ctx.Value(ctxKeySession).(*auth.Session)
	return s
}

// requireAuth is middleware that enforces a valid session token. The token is
// read from the session cookie (browser) or the Authorization: Bearer header
// (API clients). On failure it writes a 401 with CodeAuth so the SPA can show
// the login screen.
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := tokenFromRequest(r)
		if token == "" {
			writeError(w, core.AuthError("authentication required"))
			return
		}
		sess, err := s.auth.VerifyToken(r.Context(), token)
		if err != nil {
			// Always present a 401 regardless of the underlying reason
			// (tamper, expiry, missing secret) so we do not leak detail.
			writeError(w, core.AuthError("invalid or expired session"))
			return
		}
		ctx := context.WithValue(r.Context(), ctxKeySession, sess)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// tokenFromRequest extracts a session token from the cookie or Authorization
// header. The cookie takes precedence for browser flows.
func tokenFromRequest(r *http.Request) string {
	if c, err := r.Cookie(sessionCookieName); err == nil && c.Value != "" {
		return c.Value
	}
	authz := r.Header.Get("Authorization")
	if authz != "" {
		const prefix = "Bearer "
		if len(authz) > len(prefix) && strings.EqualFold(authz[:len(prefix)], prefix) {
			return strings.TrimSpace(authz[len(prefix):])
		}
	}
	return ""
}

// setSessionCookie writes the signed session token as a hardened cookie. secure
// reflects whether the request arrived over TLS so local-HTTP development still
// works while production over TLS gets the Secure flag.
func setSessionCookie(w http.ResponseWriter, token string, ttl time.Duration, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
		Expires:  time.Now().Add(ttl),
		MaxAge:   int(ttl.Seconds()),
	})
}

// clearSessionCookie expires the session cookie on logout.
func clearSessionCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
}

// isSecureRequest reports whether the request arrived over TLS (directly or via
// a trusted local reverse proxy setting X-Forwarded-Proto).
func isSecureRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// securityHeaders sets conservative headers for every response. The panel is
// a private single-admin tool; a strict CSP and frame denial are appropriate.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; "+
				"object-src 'none'; frame-ancestors 'none'; base-uri 'self'")
		next.ServeHTTP(w, r)
	})
}

// recoverer converts a panic in any handler into a 500 JSON error and logs the
// stack. Library code must not panic, but a third-party handler bug must never
// take the whole server down.
func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("panic in handler",
					"method", r.Method, "path", r.URL.Path,
					"panic", rec, "stack", string(debug.Stack()))
				writeError(w, core.InternalError("internal server error"))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// statusRecorder captures the response status for access logging.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (sr *statusRecorder) WriteHeader(code int) {
	sr.status = code
	sr.ResponseWriter.WriteHeader(code)
}

func (sr *statusRecorder) Write(b []byte) (int, error) {
	if sr.status == 0 {
		sr.status = http.StatusOK
	}
	n, err := sr.ResponseWriter.Write(b)
	sr.bytes += n
	return n, err
}

// accessLog logs one structured line per request at debug level (info for
// server errors), without leaking query strings that may contain secrets.
func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sr := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(sr, r)
		if sr.status == 0 {
			sr.status = http.StatusOK
		}
		dur := time.Since(start)
		args := []any{
			"method", r.Method,
			"path", r.URL.Path,
			"status", sr.status,
			"bytes", sr.bytes,
			"duration_ms", dur.Milliseconds(),
		}
		if sr.status >= 500 {
			s.log.Error("request", args...)
		} else {
			s.log.Debug("request", args...)
		}
	})
}
