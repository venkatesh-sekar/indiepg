package server

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/venkatesh-sekar/indiepg/internal/core"
)

// --- extensions ---

// installedExtensionResponse mirrors one installed extension for the list view.
// UpdateAvailable is true when the on-disk default version differs from the
// installed one (an ALTER EXTENSION ... UPDATE would move it forward).
type installedExtensionResponse struct {
	Name             string `json:"name"`
	InstalledVersion string `json:"installed_version"`
	DefaultVersion   string `json:"default_version"`
	UpdateAvailable  bool   `json:"update_available"`
}

// availableExtensionResponse mirrors one extension available to add, with the
// tier badge ("ready" / "needs_package" / "needs_restart") that tells the UI how
// much work the install takes and whether to warn about a restart.
type availableExtensionResponse struct {
	Name            string `json:"name"`
	Description     string `json:"description"`
	DefaultVersion  string `json:"default_version"`
	Tier            string `json:"tier"`
	RequiresPreload bool   `json:"requires_preload"`
	InCatalog       bool   `json:"in_catalog"`
	// Package is the resolved OS package (e.g. "postgresql-17-pgvector") for a
	// catalog entry that may need an apt install, so the Add dialog can preview
	// the real command. Empty for ready/free-form entries.
	Package string `json:"package"`
}

// extensionsResponse is the body of GET /api/extensions: the resolved target
// database plus its installed and available extensions.
type extensionsResponse struct {
	Database  string                       `json:"database"`
	Installed []installedExtensionResponse `json:"installed"`
	Available []availableExtensionResponse `json:"available"`
}

// handleListExtensions returns a database's installed and available extensions.
// The target database comes from the ?database= query param (defaulting to the
// maintenance database). Read-only; not audited.
func (s *Server) handleListExtensions(w http.ResponseWriter, r *http.Request) {
	database := r.URL.Query().Get("database")

	list, err := s.pg.ListExtensions(r.Context(), database)
	if err != nil {
		writeError(w, err)
		return
	}

	installed := make([]installedExtensionResponse, 0, len(list.Installed))
	for _, e := range list.Installed {
		installed = append(installed, installedExtensionResponse{
			Name:             e.Name,
			InstalledVersion: e.InstalledVersion,
			DefaultVersion:   e.DefaultVersion,
			UpdateAvailable:  e.UpdateAvailable,
		})
	}
	available := make([]availableExtensionResponse, 0, len(list.Available))
	for _, e := range list.Available {
		available = append(available, availableExtensionResponse{
			Name:            e.Name,
			Description:     e.Description,
			DefaultVersion:  e.DefaultVersion,
			Tier:            string(e.Tier),
			RequiresPreload: e.RequiresPreload,
			InCatalog:       e.InCatalog,
			Package:         e.Package,
		})
	}

	writeData(w, http.StatusOK, extensionsResponse{
		Database:  list.Database,
		Installed: installed,
		Available: available,
	})
}

// installExtensionRequest is the body for POST /api/extensions. Confirm carries
// the typed-name confirmation required by a Tier 3 (needs_restart) install; it
// is ignored for the other tiers.
type installExtensionRequest struct {
	Database string `json:"database"`
	Name     string `json:"name"`
	Confirm  string `json:"confirm"`
	// Freeform marks a request from the "add by name" field. A free-form install
	// is SQL-only: the server never apt-installs a package or edits
	// shared_preload_libraries off a typed name. Catalog Add buttons send false.
	Freeform bool `json:"freeform"`
}

// handleInstallExtension installs an extension into a database, choosing the
// tier server-side (plain CREATE, apt package, or preload+restart) and returning
// a Result recording every step that ran. A Tier 3 install without a matching
// Confirm comes back as a typed safety error the SPA renders as a confirm dialog.
func (s *Server) handleInstallExtension(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req installExtensionRequest
	if err := decodeJSON(r, &req, maxBodyBytes); err != nil {
		writeError(w, err)
		return
	}

	res, err := s.pg.InstallExtension(ctx, req.Database, req.Name, req.Confirm, req.Freeform)
	if err != nil {
		// Two different failures reach here as CodeSafety, and only one is benign.
		// The Tier 3 typed-confirmation gate (a *core.SafetyError from
		// RequireConfirmation, returned BEFORE any side effect) is an expected
		// interaction step the SPA turns into a confirm dialog — don't pollute the
		// audit trail with a phantom failure entry. But restartWithRollback ALSO
		// returns CodeSafety AFTER it has changed config, restarted, and rolled
		// back: that is a real operational event (a plain *core.Error, not a
		// SafetyError) and MUST be audited. Skip only the confirmation gate.
		var confirmGate *core.SafetyError
		if !errors.As(err, &confirmGate) {
			s.audit(ctx, "extension_install", req.Name, "failure", "install extension failed", core.CodeOf(err))
		}
		writeError(w, err)
		return
	}
	s.audit(ctx, "extension_install", req.Name, "success", "extension installed", "")
	writeData(w, http.StatusOK, res)
}

// handleUpdateExtension upgrades an installed extension to its default available
// version (ALTER EXTENSION ... UPDATE). The extension name comes from the path;
// the target database from the ?database= query param. No body or confirmation
// is required — an update is non-destructive and never restarts Postgres.
func (s *Server) handleUpdateExtension(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	name := chi.URLParam(r, "name")
	database := r.URL.Query().Get("database")

	res, err := s.pg.UpdateExtension(ctx, database, name)
	if err != nil {
		s.audit(ctx, "extension_update", name, "failure", "update extension failed", core.CodeOf(err))
		writeError(w, err)
		return
	}
	s.audit(ctx, "extension_update", name, "success", "extension updated", "")
	writeData(w, http.StatusOK, res)
}

// handleDropExtension drops an extension from a database after a typed-name
// confirmation. The extension name comes from the path; the target database from
// the ?database= query param; the confirmation from the JSON body (dropRequest).
func (s *Server) handleDropExtension(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	name := chi.URLParam(r, "name")
	database := r.URL.Query().Get("database")

	var req dropRequest
	if err := decodeJSON(r, &req, maxBodyBytes); err != nil {
		writeError(w, err)
		return
	}

	res, err := s.pg.DropExtension(ctx, database, name, req.Confirm)
	if err != nil {
		s.audit(ctx, "extension_drop", name, "failure", "drop extension failed", core.CodeOf(err))
		writeError(w, err)
		return
	}
	s.audit(ctx, "extension_drop", name, "success", "extension dropped", "")
	writeData(w, http.StatusOK, res)
}
