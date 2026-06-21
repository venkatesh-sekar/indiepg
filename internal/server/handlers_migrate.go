package server

import (
	"net/http"

	"github.com/venkatesh-sekar/indiepg/internal/core"
)

// migrationUnavailableHint explains, honestly, why every migration endpoint is
// disabled in this build. The feature has no functional backend here: there is
// no S3/object-store implementation and no pg_dump/pg_restore data-movement
// engine wired up. Rather than 404 (which the SPA would read as a routing bug)
// or pretend to succeed, each route returns a typed CodeInternal error so the
// UI can render a clear "not available" message with actionable context.
const migrationUnavailableHint = "migration requires S3 object storage and a dump/restore engine that are not configured"

// writeMigrationUnavailable writes the shared typed error used by every
// migration endpoint. It is read-only in effect (nothing is mutated and nothing
// is attempted), so no audit entry is recorded.
func writeMigrationUnavailable(w http.ResponseWriter) {
	writeError(w, core.InternalError("migration is not available in this build").
		WithHint(migrationUnavailableHint))
}

// handleCreateMigrationSession would start a new migration session. The
// migration backend is not present in this build, so it always reports the
// feature as unavailable. POST /migrate/sessions.
func (s *Server) handleCreateMigrationSession(w http.ResponseWriter, _ *http.Request) {
	writeMigrationUnavailable(w)
}

// handleGetMigrationSession would return the status of a migration session by
// its short code. Unavailable in this build. GET /migrate/sessions/{code}.
func (s *Server) handleGetMigrationSession(w http.ResponseWriter, _ *http.Request) {
	writeMigrationUnavailable(w)
}

// handleCancelMigrationSession would cancel an in-flight migration session by
// its short code. Unavailable in this build. DELETE /migrate/sessions/{code}.
func (s *Server) handleCancelMigrationSession(w http.ResponseWriter, _ *http.Request) {
	writeMigrationUnavailable(w)
}

// handleMigrateSingleDB would migrate a single database into the managed
// cluster. Unavailable in this build. POST /migrate/single-db.
func (s *Server) handleMigrateSingleDB(w http.ResponseWriter, _ *http.Request) {
	writeMigrationUnavailable(w)
}

// handleMigrateCluster would migrate an entire source cluster. Unavailable in
// this build. POST /migrate/cluster.
func (s *Server) handleMigrateCluster(w http.ResponseWriter, _ *http.Request) {
	writeMigrationUnavailable(w)
}
