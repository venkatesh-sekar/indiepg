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

			// Host-sized tuning surface (read-only): applied settings + the
			// recommendation for each workload profile.
			pr.Get("/tuning", s.handleGetTuning)
			// Apply a workload profile: the deliberate, system-mutating action that
			// resizes shared_buffers/max_connections and restarts Postgres (with
			// rollback to last-known-good). CSRF-gated POST behind requireAuth.
			pr.Post("/tuning/apply", s.handleApplyTuning)

			// Opt-in PgBouncer pooler: read-only status, plus the deliberate,
			// system-mutating enable action (installs + starts the service).
			pr.Get("/pooler", s.handleGetPoolerStatus)
			pr.Post("/pooler/enable", s.handlePoolerEnable)
			pr.Post("/pooler/disable", s.handlePoolerDisable)

			pr.Get("/audit", s.handleListAudit)

			// Dashboard (host + Postgres telemetry snapshot).
			pr.Get("/dashboard", s.handleDashboard)

			// Query box (read-only, guard-enforced).
			pr.Post("/query", s.handleQuery)

			// Databases.
			pr.Get("/databases", s.handleListDatabases)
			pr.Post("/databases", s.handleCreateDatabase)
			pr.Post("/databases/new-app", s.handleNewApp)
			pr.Delete("/databases/{name}", s.handleDropDatabase)

			// Extensions (per-database): list installed + available, install
			// (tiered: plain / apt package / preload+restart), update, and drop.
			pr.Get("/extensions", s.handleListExtensions)
			pr.Post("/extensions", s.handleInstallExtension)
			pr.Post("/extensions/{name}/update", s.handleUpdateExtension)
			pr.Delete("/extensions/{name}", s.handleDropExtension)

			// Roles & grants.
			pr.Get("/roles", s.handleListRoles)
			pr.Post("/roles", s.handleCreateRole)
			pr.Post("/roles/readonly", s.handleCreateReadonlyUser)
			pr.Post("/roles/{role}/rotate", s.handleRotatePassword)
			pr.Delete("/roles/{role}", s.handleDropRole)
			pr.Post("/grants", s.handleGrant)
			pr.Delete("/grants", s.handleRevoke)

			// Backups & restore.
			pr.Get("/backups", s.handleListBackups)
			pr.Post("/backups/run", s.handleRunBackup)
			pr.Post("/backups/restore", s.handleRestore)
			pr.Post("/backups/restore-test", s.handleRestoreTest)

			// Alerts (rules + notification channels).
			pr.Get("/alerts", s.handleGetAlerts)
			pr.Put("/alerts/rules", s.handleSaveAlertRule)
			pr.Delete("/alerts/rules/{id}", s.handleDeleteAlertRule)
			pr.Put("/alerts/channels", s.handleSaveAlertChannel)
			pr.Post("/alerts/channels/test", s.handleTestAlertChannel)

			// Migration. Two modes share one local-store job engine the UI polls:
			//   - Direct pull (single-db/cluster): pg_dump/pg_restore against a
			//     user-supplied source into this panel's Postgres. Needs NO S3.
			//   - ssh-less handshake (sessions): two panels coordinate via a shared
			//     S3 bucket. The session endpoints are the ONLY ones that require S3.
			pr.Get("/migrate", s.handleListMigrations)
			pr.Get("/migrate/{id}", s.handleGetMigration)
			pr.Post("/migrate/single-db", s.handleMigrateSingleDB)
			pr.Post("/migrate/cluster", s.handleMigrateCluster)
			pr.Post("/migrate/sessions", s.handleCreateMigrationSession)
			pr.Get("/migrate/sessions/{code}", s.handleGetMigrationSession)
			pr.Post("/migrate/sessions/{code}/export", s.handleExportMigrationSession)
			pr.Delete("/migrate/sessions/{code}", s.handleCancelMigrationSession)
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
