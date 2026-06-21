package server

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime/debug"
	"strings"
	"time"

	"github.com/venkatesh-sekar/indiepg/internal/auth"
	"github.com/venkatesh-sekar/indiepg/internal/core"
)

// trustForwardedProtoEnv, when set truthy, tells the panel it sits behind a
// trusted TLS-terminating reverse proxy so the X-Forwarded-Proto header may be
// honored. It is a deliberate, server-side opt-in: an attacker cannot set a
// process environment variable, so the spoofable header alone never decides the
// Secure cookie flag. The default (unset) trusts only r.TLS.
const trustForwardedProtoEnv = "INDIEPG_TRUST_FORWARDED_PROTO"

// csrfHeaderName is a non-simple custom header. A browser cannot attach it to a
// cross-site form/navigation request without triggering a CORS preflight, so
// its presence is sufficient proof the request came from our own SPA (which
// sets it on every XHR). It is the second accepted CSRF signal alongside an
// Origin/Referer host that matches the bind host.
const csrfHeaderName = "X-Indiepg-Csrf"

// sessionCookieName is the name of the signed session cookie. It is host-only,
// HttpOnly, SameSite=Strict, and Secure-aware so a stolen cookie cannot be read
// by JS or sent cross-site.
const sessionCookieName = "indiepg_session"

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
		token, src := tokenWithSource(r)
		if token == "" {
			writeError(w, core.AuthError("authentication required"))
			return
		}
		// CSRF defense-in-depth: SameSite=Strict on the session cookie is the
		// primary control, but for cookie-authenticated unsafe methods we also
		// require a same-origin signal so SameSite is never the single point of
		// failure. Bearer/API-token clients are immune to CSRF and skipped.
		if src == tokenSourceCookie && isUnsafeMethod(r.Method) && !s.csrfOriginOK(r) {
			writeError(w, core.NewSafetyError(
				"cross-site request",
				[]string{"same-origin Origin/Referer or " + csrfHeaderName + " header"},
				"request origin not allowed for this cookie-authenticated action",
			))
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

// isUnsafeMethod reports whether the HTTP method can change server state and so
// warrants the CSRF backstop. GET/HEAD/OPTIONS are safe by definition.
func isUnsafeMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

// csrfOriginOK reports whether a cookie-authenticated unsafe request carries an
// acceptable same-origin signal. It accepts either (a) an Origin or Referer
// whose host matches the configured bind host, or (b) a non-simple custom
// header the SPA attaches and a cross-site attacker cannot forge without a
// preflight. When neither is present the request is rejected.
func (s *Server) csrfOriginOK(r *http.Request) bool {
	if r.Header.Get(csrfHeaderName) != "" {
		return true
	}
	want := hostOnly(s.cfg.BindAddr)
	// An empty/invalid bind host means we cannot establish an expected origin;
	// fall back to requiring the custom header (already checked above), so an
	// originless request is rejected rather than blindly allowed.
	if want == "" {
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		return originHostMatches(origin, want)
	}
	if ref := r.Header.Get("Referer"); ref != "" {
		return originHostMatches(ref, want)
	}
	// No Origin and no Referer on a state-changing request: treat as untrusted.
	return false
}

// originHostMatches parses a URL (Origin or Referer value) and reports whether
// its host matches want (host comparison only; ports are ignored so a TLS proxy
// rewriting the port still matches the bind host).
func originHostMatches(rawURL, want string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return false
	}
	return strings.EqualFold(hostOnly(u.Host), want)
}

// hostOnly returns the host portion of a host:port (or bare host) string,
// lower-cased. It tolerates values without a port.
func hostOnly(hostport string) string {
	if hostport == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(hostport); err == nil {
		return strings.ToLower(host)
	}
	return strings.ToLower(hostport)
}

// tokenSource identifies where a session token was read from. It lets CSRF
// defenses apply only to browser (cookie) flows while leaving Bearer/API-token
// clients, which are immune to CSRF, untouched.
type tokenSource int

const (
	tokenSourceNone tokenSource = iota
	tokenSourceCookie
	tokenSourceBearer
)

// tokenFromRequest extracts a session token from the cookie or Authorization
// header. The cookie takes precedence for browser flows.
func tokenFromRequest(r *http.Request) string {
	token, _ := tokenWithSource(r)
	return token
}

// tokenWithSource extracts the session token and reports whether it came from
// the session cookie or the Authorization: Bearer header. The cookie takes
// precedence, matching browser flows.
func tokenWithSource(r *http.Request) (string, tokenSource) {
	if c, err := r.Cookie(sessionCookieName); err == nil && c.Value != "" {
		return c.Value, tokenSourceCookie
	}
	authz := r.Header.Get("Authorization")
	if authz != "" {
		const prefix = "Bearer "
		if len(authz) > len(prefix) && strings.EqualFold(authz[:len(prefix)], prefix) {
			return strings.TrimSpace(authz[len(prefix):]), tokenSourceBearer
		}
	}
	return "", tokenSourceNone
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

// isSecureRequest reports whether the request arrived over TLS. The
// X-Forwarded-Proto header is spoofable by any client, so it is honored ONLY
// when the operator has explicitly declared a trusted TLS-terminating proxy via
// the trustForwardedProtoEnv flag. Otherwise the Secure cookie flag is derived
// solely from r.TLS, so a direct attacker cannot strip Secure by forging the
// header (nor set it on plain HTTP to make the cookie unusable).
func isSecureRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return trustForwardedProto() && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// trustForwardedProto reports whether the operator has opted into honoring the
// X-Forwarded-Proto header from a trusted reverse proxy. It is a server-side,
// non-spoofable signal (a process environment variable), not a request header.
func trustForwardedProto() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(trustForwardedProtoEnv))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
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
