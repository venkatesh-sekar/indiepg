package core

import (
	"strings"
)

// MaxIdentifierLength is Postgres's hard limit on identifier length (bytes).
const MaxIdentifierLength = 63

// reservedWords are Postgres reserved words plus a handful of non-reserved but
// dangerous-as-names words. A validated identifier may not equal any of these
// (case-insensitively); quoting still happens regardless, this is a usability
// guard against confusing names.
var reservedWords = map[string]struct{}{
	"all": {}, "analyse": {}, "analyze": {}, "and": {}, "any": {}, "array": {},
	"as": {}, "asc": {}, "asymmetric": {}, "authorization": {}, "between": {},
	"both": {}, "case": {}, "cast": {}, "check": {}, "collate": {}, "column": {},
	"constraint": {}, "create": {}, "current_catalog": {}, "current_date": {},
	"current_role": {}, "current_schema": {}, "current_time": {},
	"current_timestamp": {}, "current_user": {}, "default": {}, "deferrable": {},
	"desc": {}, "distinct": {}, "do": {}, "else": {}, "end": {}, "except": {},
	"false": {}, "fetch": {}, "for": {}, "foreign": {}, "from": {}, "grant": {},
	"group": {}, "having": {}, "in": {}, "initially": {}, "intersect": {},
	"into": {}, "lateral": {}, "leading": {}, "limit": {}, "localtime": {},
	"localtimestamp": {}, "not": {}, "null": {}, "offset": {}, "on": {}, "only": {},
	"or": {}, "order": {}, "placing": {}, "primary": {}, "references": {},
	"returning": {}, "select": {}, "session_user": {}, "some": {}, "symmetric": {},
	"table": {}, "then": {}, "to": {}, "trailing": {}, "true": {}, "union": {},
	"unique": {}, "user": {}, "using": {}, "variadic": {}, "when": {}, "where": {},
	"window": {}, "with": {},
	// Non-reserved but problematic as object names.
	"database": {}, "index": {}, "password": {}, "role": {}, "schema": {},
	"sequence": {}, "trigger": {}, "type": {}, "view": {}, "owner": {},
	"public": {}, "template": {},
}

// ValidateIdentifier checks that value is a syntactically valid, non-reserved
// PostgreSQL identifier suitable for use as a database, role, schema, or table
// name. kind is used only in error messages (e.g. "database", "role").
//
// Rules: 1..63 chars, must start with a letter or underscore, may contain only
// ASCII letters, digits, and underscores, and must not be a reserved word.
//
// Note: this is a usability/defense gate. Code that builds SQL must STILL call
// QuoteIdent — never interpolate a validated identifier raw.
func ValidateIdentifier(value, kind string) error {
	if kind == "" {
		kind = "identifier"
	}
	if value == "" {
		return ValidationError("%s name cannot be empty", kind).
			WithHint("provide a valid name")
	}
	if len(value) > MaxIdentifierLength {
		return ValidationError("%s name exceeds maximum length (%d > %d)", kind, len(value), MaxIdentifierLength)
	}
	for i, r := range value {
		first := i == 0
		switch {
		case r == '_':
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case !first && r >= '0' && r <= '9':
		default:
			return ValidationError("invalid %s name %q", kind, value).
				WithHint("must start with a letter or underscore and contain only letters, digits, and underscores")
		}
	}
	if _, bad := reservedWords[strings.ToLower(value)]; bad {
		return ValidationError("%q is a PostgreSQL reserved word", value).
			WithHint("choose a different name, e.g. " + value + "_db")
	}
	return nil
}

// IsValidIdentifier reports whether value passes ValidateIdentifier.
func IsValidIdentifier(value string) bool {
	return ValidateIdentifier(value, "identifier") == nil
}

// QuoteIdent returns value as a safely double-quoted SQL identifier, doubling
// any embedded double quotes. It always wraps in quotes so the result is valid
// even for mixed-case or otherwise non-bare identifiers.
//
// QuoteIdent does not validate; pair it with ValidateIdentifier where the input
// is operator-supplied. It never fails, so it is safe to use when building SQL.
func QuoteIdent(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

// QuoteQualified quotes a dotted, qualified name (e.g. schema.table) part by
// part: QuoteQualified("public", "users") == `"public"."users"`. Empty parts
// are skipped, so QuoteQualified("", "users") == `"users"`.
func QuoteQualified(parts ...string) string {
	quoted := make([]string, 0, len(parts))
	for _, p := range parts {
		if p == "" {
			continue
		}
		quoted = append(quoted, QuoteIdent(p))
	}
	return strings.Join(quoted, ".")
}

// QuoteLiteral returns value as a safely single-quoted SQL string literal,
// doubling embedded single quotes. If value contains a backslash it is emitted
// using the E” escape-string syntax with backslashes doubled, matching
// PostgreSQL's quote_literal semantics.
func QuoteLiteral(value string) string {
	if strings.Contains(value, `\`) {
		escaped := strings.ReplaceAll(value, `\`, `\\`)
		escaped = strings.ReplaceAll(escaped, `'`, `''`)
		return "E'" + escaped + "'"
	}
	return "'" + strings.ReplaceAll(value, `'`, `''`) + "'"
}
