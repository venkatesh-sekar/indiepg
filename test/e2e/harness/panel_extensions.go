//go:build e2e

package harness

import (
	"net/http"
	"net/url"
)

// This file adds typed Panel methods for the per-database extension API
// (GET/POST /api/extensions). It is ADDITIVE: it does not touch the frozen
// harness core, it only builds on the exported Do/GET/POST seam in panel.go.

// ExtensionInstalled mirrors one entry of GET /api/extensions "installed".
type ExtensionInstalled struct {
	Name             string `json:"name"`
	InstalledVersion string `json:"installed_version"`
	DefaultVersion   string `json:"default_version"`
	UpdateAvailable  bool   `json:"update_available"`
}

// ExtensionAvailable mirrors one entry of GET /api/extensions "available",
// including the tier badge ("ready" / "needs_package" / "needs_restart") the
// server computes from the curated catalog plus on-disk presence.
type ExtensionAvailable struct {
	Name            string `json:"name"`
	Description     string `json:"description"`
	DefaultVersion  string `json:"default_version"`
	Tier            string `json:"tier"`
	RequiresPreload bool   `json:"requires_preload"`
	InCatalog       bool   `json:"in_catalog"`
	Package         string `json:"package"`
}

// ExtensionList is the GET /api/extensions payload for a target database.
type ExtensionList struct {
	Database  string               `json:"database"`
	Installed []ExtensionInstalled `json:"installed"`
	Available []ExtensionAvailable `json:"available"`
}

// FindInstalled returns the installed entry for name (and whether present).
func (l ExtensionList) FindInstalled(name string) (ExtensionInstalled, bool) {
	for _, e := range l.Installed {
		if e.Name == name {
			return e, true
		}
	}
	return ExtensionInstalled{}, false
}

// FindAvailable returns the available-to-add entry for name (and whether present).
func (l ExtensionList) FindAvailable(name string) (ExtensionAvailable, bool) {
	for _, e := range l.Available {
		if e.Name == name {
			return e, true
		}
	}
	return ExtensionAvailable{}, false
}

// ExtensionInstallResult mirrors the core.Result the install handler returns:
// the success flag, message, the per-action data (tier/database), and the
// recorded SQL/commands that ran (CREATE EXTENSION, apt steps, ALTER SYSTEM,
// systemctl restart).
type ExtensionInstallResult struct {
	OK         bool           `json:"ok"`
	Message    string         `json:"message"`
	Data       map[string]any `json:"data"`
	Statements []string       `json:"statements"`
}

// Tier returns the install tier the server recorded ("ready" / "needs_package"
// / "needs_restart"), or "" if absent.
func (r ExtensionInstallResult) Tier() string {
	if r.Data == nil {
		return ""
	}
	if t, ok := r.Data["tier"].(string); ok {
		return t
	}
	return ""
}

// ListExtensions fetches GET /api/extensions for a database. An empty database
// targets the maintenance database (the server default).
func (p *Panel) ListExtensions(database string) (ExtensionList, error) {
	path := "/api/extensions"
	if database != "" {
		path += "?database=" + url.QueryEscape(database)
	}
	var out ExtensionList
	err := p.GET(path, &out)
	return out, err
}

// installExtensionBody mirrors the POST /api/extensions request body. Freeform
// is always false here: these helpers drive the curated catalog Add path (the
// full tiered orchestration), never the SQL-only "add by name" path.
type installExtensionBody struct {
	Database string `json:"database"`
	Name     string `json:"name"`
	Confirm  string `json:"confirm,omitempty"`
	Freeform bool   `json:"freeform"`
}

// InstallExtension performs a catalog install (POST /api/extensions). confirm
// carries the typed-name confirmation a Tier 3 (needs_restart) install requires;
// pass "" for ready / needs_package tiers. It returns a typed error on any
// non-2xx (e.g. the Tier 3 confirmation gate comes back as a *PanelError with
// Code "safety").
func (p *Panel) InstallExtension(database, name, confirm string) (ExtensionInstallResult, error) {
	var out ExtensionInstallResult
	err := p.POST("/api/extensions", installExtensionBody{
		Database: database,
		Name:     name,
		Confirm:  confirm,
	}, &out)
	return out, err
}

// InstallExtensionResp performs a catalog install and returns the raw Response
// without decoding, so a scenario can assert on the HTTP status of the Tier 3
// confirmation gate directly.
func (p *Panel) InstallExtensionResp(database, name, confirm string) (*Response, error) {
	return p.Do(http.MethodPost, "/api/extensions", installExtensionBody{
		Database: database,
		Name:     name,
		Confirm:  confirm,
	})
}
