package pg

// This file is the single source of truth for which PostgreSQL major versions
// the panel knows how to install and upgrade to. It drives both the installer's
// version picker (cmd/indiepg install --pg-version) and the major-upgrade target
// list shown in the Version panel. Adding support for a new PG major to the
// whole panel is a one-line edit here.

// MajorRelease describes a PostgreSQL major version the panel supports.
type MajorRelease struct {
	// Major is the PostgreSQL major version number (e.g. 17).
	Major int
	// Default marks the latest stable release the panel recommends. Exactly one
	// entry in SupportedMajors should carry Default=true.
	Default bool
	// EOL is informational: an end-of-life major is still installable and
	// upgradable (so an operator stuck on it can move forward), but the UI may
	// nudge an upgrade. It does not gate anything.
	EOL bool
}

// SupportedMajors is the curated set of PostgreSQL majors the panel can install
// and upgrade between, newest first. The PGDG apt repository ships every one of
// these side-by-side, which is what makes both version selection and
// pg_upgradecluster possible. Adding a new major is a single entry here.
var SupportedMajors = []MajorRelease{
	{Major: 17, Default: true},
	{Major: 16},
	{Major: 15},
}

// DefaultMajor returns the major version marked Default in SupportedMajors — the
// version a fresh install picks when none is requested. It falls back to the
// numerically highest supported major if no entry is flagged (defensive: the
// catalog should always have exactly one default).
func DefaultMajor() int {
	highest := 0
	for _, r := range SupportedMajors {
		if r.Default {
			return r.Major
		}
		if r.Major > highest {
			highest = r.Major
		}
	}
	return highest
}

// IsSupported reports whether major is a version the panel knows how to install
// and upgrade to.
func IsSupported(major int) bool {
	for _, r := range SupportedMajors {
		if r.Major == major {
			return true
		}
	}
	return false
}

// LookupMajor returns the catalog entry for a major version and whether it was
// found.
func LookupMajor(major int) (MajorRelease, bool) {
	for _, r := range SupportedMajors {
		if r.Major == major {
			return r, true
		}
	}
	return MajorRelease{}, false
}

// MajorsNewerThan returns the supported majors strictly greater than current, in
// the catalog's order (newest first). It is the upgrade-target list for a
// cluster currently on `current`.
func MajorsNewerThan(current int) []MajorRelease {
	out := make([]MajorRelease, 0, len(SupportedMajors))
	for _, r := range SupportedMajors {
		if r.Major > current {
			out = append(out, r)
		}
	}
	return out
}
