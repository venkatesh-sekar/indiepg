package pg

import "strings"

// MergePreloadLibraries computes the new value of the shared_preload_libraries
// GUC after ensuring lib is present, returning the merged value and whether it
// changed. It is a pure read-modify-write: existing entries and their order are
// preserved, and the operation is idempotent — if lib is already loaded the
// current value is returned unchanged with changed=false, so callers can skip a
// needless ALTER SYSTEM + restart.
//
// shared_preload_libraries is a comma-separated list whose items may carry
// surrounding whitespace and may be double-quoted (e.g. `pg_stat_statements,
// "pg_cron"`). Parsing tolerates all of those forms for the presence check; the
// appended entry is emitted bare, since library names are plain identifiers.
func MergePreloadLibraries(current, lib string) (newValue string, changed bool) {
	lib = strings.TrimSpace(lib)
	if lib == "" {
		return current, false
	}
	for _, e := range parsePreloadList(current) {
		if e == lib {
			return current, false
		}
	}
	trimmed := strings.TrimSpace(current)
	if trimmed == "" {
		return lib, true
	}
	return trimmed + ", " + lib, true
}

// parsePreloadList splits a shared_preload_libraries value into its entry names,
// trimming whitespace and any surrounding double quotes and dropping empties.
func parsePreloadList(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, raw := range parts {
		e := strings.TrimSpace(raw)
		e = strings.Trim(e, `"`)
		e = strings.TrimSpace(e)
		if e != "" {
			out = append(out, e)
		}
	}
	return out
}
