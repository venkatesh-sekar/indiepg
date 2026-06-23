package server

import (
	"net/http"
	"time"

	"github.com/venkatesh-sekar/indiepg/internal/auth"
	"github.com/venkatesh-sekar/indiepg/internal/core"
)

// loginRequest is the JSON body for POST /api/auth/login.
type loginRequest struct {
	Password string `json:"password"`
}

// loginResponse is returned on a successful login. The token is also set as a
// hardened session cookie; API clients without cookie jars can read it here.
type loginResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// handleLogin verifies the admin password (applying lockout) and, on success,
// issues a signed session token via cookie + body. Lockout returns CodeLocked
// (HTTP 429); a bad password returns CodeAuth (HTTP 401). The audit log records
// both outcomes without ever recording the password.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := decodeJSON(r, &req, maxBodyBytes); err != nil {
		writeError(w, err)
		return
	}
	if req.Password == "" {
		writeError(w, core.ValidationError("password is required"))
		return
	}

	ctx := r.Context()
	token, err := s.auth.Authenticate(ctx, req.Password)
	if err != nil {
		s.audit(ctx, "login", "auth", "failure", "admin login failed", core.CodeOf(err))
		writeError(w, err)
		return
	}

	s.audit(ctx, "login", "auth", "success", "admin logged in", "")
	setSessionCookie(w, token, s.sessionTTL, isSecureRequest(r))
	writeData(w, http.StatusOK, loginResponse{
		Token:     token,
		ExpiresAt: time.Now().Add(s.sessionTTL).UTC(),
	})
}

// handleLogout clears the session cookie and, when the caller proves it holds
// the live session, invalidates that session server-side by rotating the
// signing secret so the just-used token can never be replayed. It stays public
// and idempotent for cookie-clearing — a logout from an expired or absent
// session still clears the stale cookie — but only an authenticated caller (and,
// for cookie flows, one passing the CSRF origin check) triggers the rotation,
// so the public endpoint cannot be abused to force-invalidate the admin (DoS).
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if s.logoutAuthorized(r) {
		if err := s.auth.Logout(r.Context()); err != nil {
			writeError(w, err)
			return
		}
		s.audit(r.Context(), "logout", "auth", "success", "admin logged out", "")
	}
	clearSessionCookie(w, isSecureRequest(r))
	writeData(w, http.StatusOK, map[string]any{"ok": true})
}

// logoutAuthorized reports whether a logout request proves it comes from the
// live session holder and so may invalidate the session server-side. A cookie
// flow must additionally pass the CSRF origin check (a Bearer flow is immune to
// CSRF). A missing, expired, tampered, or cross-origin credential returns false:
// the cookie is still cleared, but no server-side rotation occurs. Logout is
// always POST, so requireAuth's isUnsafeMethod guard is implicit here — the CSRF
// check applies to every cookie-sourced call.
func (s *Server) logoutAuthorized(r *http.Request) bool {
	token, src := tokenWithSource(r)
	if token == "" {
		return false
	}
	if src == tokenSourceCookie && !s.csrfOriginOK(r) {
		return false
	}
	_, err := s.auth.VerifyToken(r.Context(), token)
	return err == nil
}

// authStatusResponse reports whether the caller is authenticated and, when an
// account is locked, until when. It never reveals whether a password is set in
// a way that aids enumeration beyond the install state the operator controls.
type authStatusResponse struct {
	Authenticated bool       `json:"authenticated"`
	Installed     bool       `json:"installed"`
	Locked        bool       `json:"locked"`
	LockedUntil   *time.Time `json:"locked_until,omitempty"`
}

// handleAuthStatus is a public endpoint the SPA polls on load to decide between
// the login screen, the dashboard, or a "not installed" first-run screen.
func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	resp := authStatusResponse{}

	if sess := s.sessionFromCookie(r); sess != nil {
		resp.Authenticated = true
	}

	// Installed = an auth record exists. A NotFound from the store means the
	// panel has not run install yet.
	if _, err := s.store.GetAuth(ctx); err == nil {
		resp.Installed = true
	}

	if locked, until, err := s.auth.IsLocked(ctx); err == nil && locked {
		resp.Locked = true
		u := until.UTC()
		resp.LockedUntil = &u
	}

	writeData(w, http.StatusOK, resp)
}

// whoamiResponse identifies the authenticated admin for the UI header.
type whoamiResponse struct {
	Subject   string    `json:"subject"`
	IssuedAt  time.Time `json:"issued_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// handleWhoami returns the current session details. It runs behind requireAuth,
// so a session is always present; it reads it from the request context.
func (s *Server) handleWhoami(w http.ResponseWriter, r *http.Request) {
	sess := sessionFromContext(r.Context())
	if sess == nil {
		writeError(w, core.AuthError("authentication required"))
		return
	}
	writeData(w, http.StatusOK, whoamiResponse{
		Subject:   sess.Subject,
		IssuedAt:  sess.IssuedAt.UTC(),
		ExpiresAt: sess.ExpiresAt.UTC(),
	})
}

// sessionFromCookie attempts to validate the request's session without failing
// the request; it is used by status endpoints that report auth state rather
// than gate access. It returns nil when unauthenticated.
func (s *Server) sessionFromCookie(r *http.Request) *auth.Session {
	token := tokenFromRequest(r)
	if token == "" {
		return nil
	}
	sess, err := s.auth.VerifyToken(r.Context(), token)
	if err != nil {
		return nil
	}
	return sess
}
