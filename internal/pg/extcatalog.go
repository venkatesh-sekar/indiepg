package pg

import (
	"fmt"
	"strings"
)

// CatalogEntry describes a curated, well-known PostgreSQL extension the panel
// can install with one click. The catalog is a small, hand-maintained Go table
// rather than a config file: it carries the metadata needed to install an
// extension on a Debian/Ubuntu host — the OS package that ships the extension's
// files and whether it needs a shared_preload_libraries entry plus a restart.
//
// Entries are pure data; no field is operator-supplied, so the package
// templates are safe to feed (as an exec arg vector) to apt.
type CatalogEntry struct {
	// Name is the extension name as used in CREATE EXTENSION, e.g. "vector".
	Name string
	// Description is a one-line summary shown in the "available to add" list.
	Description string
	// PackageTemplate is the Debian/Ubuntu package that ships the extension's
	// files. When it contains a "%d" verb it is filled with the cluster's PG
	// major version (e.g. "postgresql-%d-pgvector" → "postgresql-17-pgvector");
	// contrib modules ship with the version-agnostic "postgresql-contrib"
	// family and have no verb. Resolve it via PackageName.
	PackageTemplate string
	// RequiresPreload is true when the extension must be listed in
	// shared_preload_libraries (which requires a Postgres restart to take
	// effect) before CREATE EXTENSION will succeed.
	RequiresPreload bool
	// PreloadLib is the library name to add to shared_preload_libraries. It is
	// empty unless RequiresPreload is true, and is usually — but not always —
	// equal to Name (e.g. extension "vector" loads library "vector").
	PreloadLib string
}

// PackageName resolves the entry's Debian/Ubuntu package for the given PG major
// version. Templates that carry a "%d" verb are filled with major; contrib
// packages (no verb) are returned verbatim.
func (e CatalogEntry) PackageName(major int) string {
	if strings.Contains(e.PackageTemplate, "%d") {
		return fmt.Sprintf(e.PackageTemplate, major)
	}
	return e.PackageTemplate
}

// contribPackage is the Debian/Ubuntu package family that ships PostgreSQL's
// bundled "contrib" modules (pg_stat_statements, hstore, uuid-ossp, citext,
// pg_trgm, btree_gin, btree_gist, …). On modern PGDG builds these files are
// installed alongside the server, so they are usually already on disk; the
// package is recorded for the rare host that omitted contrib.
const contribPackage = "postgresql-contrib"

// Catalog is the curated set of popular extensions offered for one-click
// install. It is intentionally small and ordered roughly by how commonly an
// indie operator reaches for each one. Anything outside this list is still
// installable via the free-form "add by name" path (SQL-only, no apt).
var Catalog = []CatalogEntry{
	{
		Name:            "vector",
		Description:     "Vector similarity search for embeddings (pgvector).",
		PackageTemplate: "postgresql-%d-pgvector",
	},
	{
		Name:            "postgis",
		Description:     "Geographic objects and spatial queries (PostGIS).",
		PackageTemplate: "postgresql-%d-postgis-3",
	},
	{
		Name:            "pg_stat_statements",
		Description:     "Tracks planning and execution statistics of SQL statements.",
		PackageTemplate: contribPackage,
		RequiresPreload: true,
		PreloadLib:      "pg_stat_statements",
	},
	{
		Name:            "pg_cron",
		Description:     "Cron-based job scheduler that runs inside Postgres.",
		PackageTemplate: "postgresql-%d-cron",
		RequiresPreload: true,
		PreloadLib:      "pg_cron",
	},
	{
		Name:            "hstore",
		Description:     "Key/value pairs stored in a single column.",
		PackageTemplate: contribPackage,
	},
	{
		Name:            "uuid-ossp",
		Description:     "Functions to generate universally unique identifiers (UUIDs).",
		PackageTemplate: contribPackage,
	},
	{
		Name:            "citext",
		Description:     "Case-insensitive character string type.",
		PackageTemplate: contribPackage,
	},
	{
		Name:            "pg_trgm",
		Description:     "Trigram-based text similarity and indexed LIKE/ILIKE.",
		PackageTemplate: contribPackage,
	},
	{
		Name:            "btree_gin",
		Description:     "GIN index support for common B-tree data types.",
		PackageTemplate: contribPackage,
	},
	{
		Name:            "btree_gist",
		Description:     "GiST index support for common B-tree data types.",
		PackageTemplate: contribPackage,
	},
}

// LookupCatalog returns the catalog entry for name and whether it was found.
func LookupCatalog(name string) (CatalogEntry, bool) {
	for _, e := range Catalog {
		if e.Name == name {
			return e, true
		}
	}
	return CatalogEntry{}, false
}
