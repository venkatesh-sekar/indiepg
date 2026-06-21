package server

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// buildRouter composes the chi router: global middleware, the public health and
// auth endpoints, the authenticated /api surface, and the SPA fallback for
// everything else. The route table is the contract the SPA talks to.
func (s *Server) buildRouter() http.Handler {
	r := chi.NewRouter()

	// Global middleware (outermost first): recover from panics, attach a
	// request id, set security headers, then log.
	r.Use(s.recoverer)
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(securityHeaders)
	r.Use(s.accessLog)

	// Public, unauthenticated endpoints used by load balancers and the login
	// screen. /healthz is liveness; /readyz checks the store.
	r.Get("/healthz", s.handleLiveness)
	r.Get("/readyz", s.handleReadiness)

	// JSON API. Everything under /api except the login/status endpoints
	// requires a valid session.
	r.Route("/api", func(api chi.Router) {
		// Public auth endpoints.
		api.Post("/auth/login", s.handleLogin)
		api.Post("/auth/logout", s.handleLogout)
		api.Get("/auth/status", s.handleAuthStatus)

		// Authenticated endpoints.
		api.Group(func(pr chi.Router) {
			pr.Use(s.requireAuth)

			pr.Get("/version", s.handleVersion)
			pr.Get("/instance", s.handleInstance)
			pr.Get("/health", s.handleHealth)
			pr.Get("/auth/whoami", s.handleWhoami)

			pr.Get("/config", s.handleGetConfig)
			pr.Put("/config", s.handleUpdateConfig)

			pr.Get("/audit", s.handleListAudit)
		})
	})

	// Anything not matched above is handed to the SPA (client-side routing).
	r.NotFound(s.spa.ServeHTTP)
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		// For API paths a wrong method should be a JSON 405, not the SPA.
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	})

	return r
}
