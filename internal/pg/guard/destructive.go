package guard

import "strings"

// detectDestructive inspects an already-tokenized write/DDL statement and reports
// whether it is destructive and, if so, the object name the operator should be
// asked to type to confirm.
//
// Destructive operations (matching the design's "typed-name confirmation" rule):
//   - DROP <object> ...        -> the dropped object's name
//   - TRUNCATE [TABLE] <tbl>   -> the truncated table's name
//   - DELETE FROM <tbl> with no WHERE  -> the table (whole-table delete)
//   - UPDATE <tbl> ... with no WHERE   -> the table (whole-table update)
//   - ALTER ... DROP <col/constraint>  -> the altered object (data-affecting)
//
// Statements with a WHERE clause, or non-destructive DDL (CREATE, GRANT, ALTER
// that only adds), are not flagged.
func detectDestructive(toks []token) (bool, string) {
	verb := ""
	for _, t := range toks {
		if t.kind == tokWord {
			verb = t.upper()
			break
		}
	}

	switch verb {
	case "DROP":
		return true, dropTarget(toks)
	case "TRUNCATE":
		return true, truncateTarget(toks)
	case "DELETE":
		if hasTopLevelWhere(toks) {
			return false, ""
		}
		return true, deleteTarget(toks)
	case "UPDATE":
		if hasTopLevelWhere(toks) {
			return false, ""
		}
		return true, updateTarget(toks)
	case "ALTER":
		destructive, target := alterDestructive(toks)
		if !destructive {
			return false, ""
		}
		return true, target
	default:
		return false, ""
	}
}

// hasTopLevelWhere reports whether a WHERE clause exists at parenthesis depth 0.
// A subquery's WHERE (depth > 0) does not bound the outer DELETE/UPDATE.
func hasTopLevelWhere(toks []token) bool {
	for _, t := range toks {
		if t.depth == 0 && t.isWord("WHERE") {
			return true
		}
	}
	return false
}

// dropTarget extracts the primary object name from a DROP statement. It skips
// the object-type keyword(s) and optional IF EXISTS, then takes the first
// identifier (possibly schema-qualified). Returns "" if none found.
func dropTarget(toks []token) string {
	i := indexAfterWord(toks, 0) // past DROP
	// skip object-type keywords: TABLE, INDEX, MATERIALIZED VIEW, etc.
	i = skipObjectType(toks, i)
	i = skipIfExists(toks, i)
	return qualifiedNameAt(toks, i)
}

// truncateTarget extracts the table name from a TRUNCATE statement, skipping an
// optional TABLE keyword and ONLY.
func truncateTarget(toks []token) string {
	i := indexAfterWord(toks, 0) // past TRUNCATE
	i = skipWordIf(toks, i, "TABLE")
	i = skipWordIf(toks, i, "ONLY")
	return qualifiedNameAt(toks, i)
}

// deleteTarget extracts the table name from a DELETE statement: DELETE FROM ONLY? tbl.
func deleteTarget(toks []token) string {
	i := wordIndex(toks, 0, "FROM")
	if i < 0 {
		return ""
	}
	i++
	i = skipWordIf(toks, i, "ONLY")
	return qualifiedNameAt(toks, i)
}

// updateTarget extracts the table name from an UPDATE statement: UPDATE ONLY? tbl.
func updateTarget(toks []token) string {
	i := indexAfterWord(toks, 0) // past UPDATE
	i = skipWordIf(toks, i, "ONLY")
	return qualifiedNameAt(toks, i)
}

// alterDestructive reports whether an ALTER statement is data-affecting, and the
// target object. ALTER ... DROP COLUMN/CONSTRAINT and ALTER COLUMN ... TYPE are
// destructive (they can lose data); plain additions/renames are not.
func alterDestructive(toks []token) (bool, string) {
	// target is the object after the object-type keyword: ALTER TABLE ONLY? tbl
	i := indexAfterWord(toks, 0) // past ALTER
	objStart := skipObjectType(toks, i)
	objStart = skipIfExists(toks, objStart)
	objStart = skipWordIf(toks, objStart, "ONLY")
	target := qualifiedNameAt(toks, objStart)

	// scan the action clause for destructive operations at top level.
	for j := 0; j < len(toks); j++ {
		t := toks[j]
		if t.depth != 0 || t.kind != tokWord {
			continue
		}
		switch t.upper() {
		case "DROP":
			// ALTER TABLE t DROP COLUMN c / DROP CONSTRAINT k / DROP <name>
			return true, target
		case "TYPE":
			// ALTER TABLE t ALTER COLUMN c TYPE ... (potential data loss/rewrite)
			// Only treat as destructive when it is the column-type form: there is
			// an ALTER ... COLUMN earlier. Be conservative and flag any top-level
			// TYPE following an ALTER COLUMN.
			if hasAlterColumn(toks) {
				return true, target
			}
		}
	}
	return false, target
}

// hasAlterColumn reports whether the token stream contains an ALTER COLUMN
// sub-clause (used to scope destructive TYPE changes).
func hasAlterColumn(toks []token) bool {
	for j := 0; j+1 < len(toks); j++ {
		if toks[j].isWord("ALTER") && j > 0 {
			// the leading ALTER is the statement verb; we want a later one
			k := j + 1
			if k < len(toks) && (toks[k].isWord("COLUMN") || toks[k].kind == tokWord || toks[k].kind == tokQuoted) {
				return true
			}
		}
	}
	return false
}

// --- token navigation helpers ---

// indexAfterWord returns the index just after the first word token at or after
// start. If none, returns len(toks).
func indexAfterWord(toks []token, start int) int {
	for i := start; i < len(toks); i++ {
		if toks[i].kind == tokWord {
			return i + 1
		}
	}
	return len(toks)
}

// wordIndex returns the index of the first word token (at or after start) equal
// to kw (case-insensitive), or -1.
func wordIndex(toks []token, start int, kw string) int {
	for i := start; i < len(toks); i++ {
		if toks[i].isWord(kw) {
			return i
		}
	}
	return -1
}

// skipWordIf advances past one word token at i if it equals kw.
func skipWordIf(toks []token, i int, kw string) int {
	if i < len(toks) && toks[i].isWord(kw) {
		return i + 1
	}
	return i
}

// skipIfExists advances past an optional "IF EXISTS" / "IF NOT EXISTS" at i.
func skipIfExists(toks []token, i int) int {
	if i < len(toks) && toks[i].isWord("IF") {
		i++
		i = skipWordIf(toks, i, "NOT")
		i = skipWordIf(toks, i, "EXISTS")
	}
	return i
}

// objectTypeWords are the leading object-type keywords that can follow DROP /
// ALTER before the object name. MATERIALIZED is followed by VIEW; both are
// skipped by skipObjectType.
var objectTypeWords = map[string]struct{}{
	"TABLE": {}, "INDEX": {}, "VIEW": {}, "SEQUENCE": {}, "SCHEMA": {},
	"DATABASE": {}, "ROLE": {}, "USER": {}, "TYPE": {}, "DOMAIN": {},
	"FUNCTION": {}, "PROCEDURE": {}, "TRIGGER": {}, "EXTENSION": {},
	"TABLESPACE": {}, "PUBLICATION": {}, "SUBSCRIPTION": {}, "SERVER": {},
	"AGGREGATE": {}, "COLLATION": {}, "CONVERSION": {}, "POLICY": {},
	"RULE": {}, "STATISTICS": {}, "FOREIGN": {},
}

// skipObjectType advances past one (or two, for MATERIALIZED VIEW / FOREIGN
// TABLE) object-type keyword(s) at i, returning the index of the object name.
func skipObjectType(toks []token, i int) int {
	if i >= len(toks) || toks[i].kind != tokWord {
		return i
	}
	w := toks[i].upper()
	if w == "MATERIALIZED" {
		i++
		return skipWordIf(toks, i, "VIEW")
	}
	if w == "FOREIGN" {
		i++
		return skipWordIf(toks, i, "TABLE")
	}
	if _, ok := objectTypeWords[w]; ok {
		return i + 1
	}
	return i
}

// qualifiedNameAt reads a possibly schema-qualified identifier starting at index
// i (e.g. public.users or just users), returning its rendered name. Quoted
// identifiers contribute their unquoted text. Returns "" if no identifier is at
// i.
func qualifiedNameAt(toks []token, i int) string {
	if i >= len(toks) {
		return ""
	}
	first, ok := identText(toks[i])
	if !ok {
		return ""
	}
	parts := []string{first}
	j := i + 1
	for j+1 < len(toks) && toks[j].kind == tokPunct && toks[j].text == "." {
		next, ok := identText(toks[j+1])
		if !ok {
			break
		}
		parts = append(parts, next)
		j += 2
	}
	return strings.Join(parts, ".")
}

// identText returns the identifier text of a token (bare word or quoted
// identifier) and whether the token can serve as an identifier.
func identText(t token) (string, bool) {
	switch t.kind {
	case tokWord:
		return t.text, true
	case tokQuoted:
		return t.text, true
	default:
		return "", false
	}
}
