// Package guard provides SQL statement classification and read-only enforcement
// for the pgpanel query box. It is the UI-layer guard rail that complements the
// database-level read-only role (the read-only role is the authoritative
// defense; this package is a fast, helpful second layer).
//
// Responsibilities:
//   - Classify a single SQL statement as read / write / DDL / utility / unknown
//     and detect whether it is destructive (DROP / TRUNCATE / DELETE-without-WHERE
//     / UPDATE-without-WHERE / data-affecting ALTER).
//   - Reject non-read statements when the guard is in read-only mode, returning a
//     *core.Error with code CodeSafety.
//   - Inject a top-level LIMIT into unbounded SELECTs so a beginner cannot pull
//     millions of rows into the browser by accident.
//   - Surface a DestructiveError carrying the object name the operator must type
//     to confirm a destructive operation.
//
// Nothing here executes SQL; it only inspects and (for LIMIT) rewrites text.
// It never panics and uses a forgiving lexer, so malformed input degrades to
// ClassUnknown rather than failing.
package guard

import (
	"strconv"
	"strings"

	"github.com/venkatesh-sekar/pgpanel/internal/core"
)

// Class is the safety classification of a single SQL statement.
type Class int

const (
	// ClassRead is a pure read: SELECT / SHOW / EXPLAIN (without a writing FOR
	// clause) / WITH ... SELECT / VALUES / TABLE. Safe to run as the read-only
	// role.
	ClassRead Class = iota
	// ClassWrite mutates data: INSERT / UPDATE / DELETE / MERGE / COPY ... FROM.
	ClassWrite
	// ClassDDL changes schema or permissions: CREATE / ALTER / DROP / TRUNCATE /
	// GRANT / REVOKE / COMMENT / REINDEX / VACUUM / CLUSTER, etc.
	ClassDDL
	// ClassUtility is a session/transaction control statement with no data or
	// schema effect: SET / RESET / BEGIN / COMMIT / ROLLBACK / DISCARD, etc.
	ClassUtility
	// ClassUnknown is anything the guard cannot confidently classify (empty
	// input, comments only, or an unrecognized leading keyword).
	ClassUnknown
)

// String returns a stable lower-case label for the class.
func (c Class) String() string {
	switch c {
	case ClassRead:
		return "read"
	case ClassWrite:
		return "write"
	case ClassDDL:
		return "ddl"
	case ClassUtility:
		return "utility"
	default:
		return "unknown"
	}
}

// IsReadOnly reports whether the class is safe to run as a read-only role.
// Only ClassRead qualifies; utility statements (e.g. SET) are not considered
// read-only because some can change session behavior in surprising ways and
// must go through the guided path, not the query box.
func (c Class) IsReadOnly() bool { return c == ClassRead }

// Classification is the result of analyzing a single statement.
type Classification struct {
	Class         Class
	Statement     string // the (trimmed) statement that was analyzed
	HasLimit      bool   // a top-level LIMIT clause is present
	IsDestructive bool   // DROP / TRUNCATE / DELETE-or-UPDATE-without-WHERE / data ALTER
	Target        string // object the destructive op acts on, for typed-name confirm
}

// Classify analyzes a single SQL statement and reports its safety class plus the
// LIMIT/destructive metadata the guard needs. It examines only the leading
// keywords and top-level clauses, so it is fast and never executes anything.
func Classify(sql string) Classification {
	stmt := trimStatement(sql)
	cls := Classification{Statement: stmt, Class: ClassUnknown}
	if stmt == "" {
		return cls
	}

	toks := tokenize(stmt)
	cls.HasLimit = hasTopLevelLimit(toks)

	lead := firstWords(toks, 3)
	if len(lead) == 0 {
		return cls
	}

	switch lead[0] {
	// ---- reads ----
	case "SELECT", "TABLE", "VALUES":
		cls.Class = ClassRead
	case "SHOW":
		cls.Class = ClassUtility
	case "EXPLAIN":
		// EXPLAIN is read-only UNLESS it carries ANALYZE, which actually runs the
		// (possibly writing) statement. Treat EXPLAIN ANALYZE conservatively by
		// classifying the inner statement.
		cls.Class = classifyExplain(toks)
	case "WITH":
		// A CTE is as dangerous as its final statement: WITH ... DELETE ... is a
		// write. Find the outermost top-level leading verb after the CTE list.
		cls.Class = classifyWith(toks)

	// ---- writes ----
	case "INSERT", "UPDATE", "DELETE", "MERGE":
		cls.Class = ClassWrite
	case "COPY":
		cls.Class = classifyCopy(toks)

	// ---- ddl ----
	case "CREATE", "ALTER", "DROP", "TRUNCATE", "GRANT", "REVOKE",
		"COMMENT", "REINDEX", "CLUSTER", "REFRESH", "SECURITY",
		"IMPORT", "REASSIGN", "VACUUM", "ANALYZE", "ANALYSE",
		"LOCK", "LISTEN", "NOTIFY", "UNLISTEN":
		cls.Class = ClassDDL

	// ---- utility (session / transaction control) ----
	case "SET", "RESET", "SHOW_ALL", "BEGIN", "START", "COMMIT", "END",
		"ROLLBACK", "ABORT", "SAVEPOINT", "RELEASE", "DISCARD",
		"PREPARE", "EXECUTE", "DEALLOCATE", "DECLARE", "FETCH",
		"CLOSE", "MOVE", "CHECKPOINT", "LOAD":
		cls.Class = ClassUtility

	default:
		cls.Class = ClassUnknown
	}

	// Destructive detection is independent of read/write class but only relevant
	// for write/DDL statements.
	if cls.Class == ClassWrite || cls.Class == ClassDDL {
		cls.IsDestructive, cls.Target = detectDestructive(toks)
	}
	return cls
}

// classifyExplain classifies an EXPLAIN: read-only unless ANALYZE is present, in
// which case the inner statement's class governs.
func classifyExplain(toks []token) Class {
	// Skip EXPLAIN and an optional ( ... ) options block, then look at the inner
	// statement. If ANALYZE appears (as a word or inside the options), the inner
	// statement actually executes.
	analyze := false
	idx := 1
	if idx < len(toks) && toks[idx].text == "(" {
		// option list: EXPLAIN (ANALYZE, BUFFERS) SELECT ...
		depth := toks[idx].depth
		idx++
		for idx < len(toks) {
			if toks[idx].depth == depth && toks[idx].text == ")" {
				idx++
				break
			}
			if toks[idx].isWord("ANALYZE") || toks[idx].isWord("ANALYSE") {
				analyze = true
			}
			idx++
		}
	} else {
		// EXPLAIN ANALYZE SELECT ... / EXPLAIN VERBOSE SELECT ...
		for idx < len(toks) {
			t := toks[idx]
			if t.isWord("ANALYZE") || t.isWord("ANALYSE") {
				analyze = true
				idx++
				continue
			}
			if t.isWord("VERBOSE") {
				idx++
				continue
			}
			break
		}
	}
	if !analyze {
		return ClassRead
	}
	inner := classFromVerb(wordAt(toks, idx))
	if inner == ClassUnknown {
		// Be conservative: an EXPLAIN ANALYZE of something we cannot read is not
		// safe to call read-only.
		return ClassWrite
	}
	return inner
}

// classifyWith resolves the class of a WITH ... statement by finding the
// outermost (depth-0) statement verb that follows the CTE definitions. A CTE may
// itself contain a data-modifying statement, so we scan for the first top-level
// write/ddl verb; absent that, it is a read.
func classifyWith(toks []token) Class {
	// Any data-modifying statement can appear either as the primary statement
	// after the CTE list, or inside a CTE: WITH t AS (DELETE ... RETURNING ...).
	// Postgres treats the whole thing as data-modifying. So if ANY depth-0-or-1
	// leading verb is a writer, classify as write.
	cls := ClassRead
	for i := 1; i < len(toks); i++ {
		t := toks[i]
		if t.kind != tokWord {
			continue
		}
		// only consider verbs that start a (sub)statement: at the very top, or
		// right after an opening paren of a CTE body.
		atStmtStart := t.depth == 0 || (i > 0 && toks[i-1].text == "(")
		if !atStmtStart {
			continue
		}
		switch t.upper() {
		case "INSERT", "UPDATE", "DELETE", "MERGE":
			return ClassWrite
		case "CREATE", "ALTER", "DROP", "TRUNCATE":
			return ClassDDL
		}
	}
	return cls
}

// classifyCopy distinguishes COPY ... FROM (a write into a table) from
// COPY ... TO (a read out of a table or query). FROM is a write; TO is a read.
func classifyCopy(toks []token) Class {
	for i := 1; i < len(toks); i++ {
		if toks[i].isWord("FROM") {
			return ClassWrite
		}
		if toks[i].isWord("TO") {
			return ClassRead
		}
	}
	// Bare COPY without FROM/TO is malformed; treat conservatively as a write.
	return ClassWrite
}

// classFromVerb maps a leading verb word to a class (used for the inner
// statement of EXPLAIN ANALYZE).
func classFromVerb(verb string) Class {
	switch strings.ToUpper(verb) {
	case "SELECT", "TABLE", "VALUES", "WITH":
		return ClassRead
	case "INSERT", "UPDATE", "DELETE", "MERGE", "COPY":
		return ClassWrite
	case "CREATE", "ALTER", "DROP", "TRUNCATE", "GRANT", "REVOKE":
		return ClassDDL
	case "":
		return ClassUnknown
	default:
		return ClassUnknown
	}
}

// Options configure the guard's enforcement behavior.
type Options struct {
	// ReadOnly rejects anything whose class is not read-only.
	ReadOnly bool
	// AutoLimit injects LIMIT N into unbounded top-level SELECTs. 0 disables.
	AutoLimit int
}

// Guard enforces classification policy: read-only rejection and auto-LIMIT.
type Guard struct {
	opts Options
}

// New returns a Guard configured with opts.
func New(opts Options) *Guard {
	if opts.AutoLimit < 0 {
		opts.AutoLimit = 0
	}
	return &Guard{opts: opts}
}

// Options returns the guard's configured options.
func (g *Guard) Options() Options { return g.opts }

// Check classifies sql and applies policy. When the guard is read-only and the
// statement is not read-only, it returns a *core.Error with code CodeSafety (and
// the classification, for context). When auto-LIMIT is enabled and the statement
// is an unbounded read, the returned SQL has a top-level LIMIT injected; this is
// the SQL the caller should execute.
//
// Check never executes anything and never panics.
func (g *Guard) Check(sql string) (rewritten string, cls Classification, err error) {
	cls = Classify(sql)

	if g.opts.ReadOnly && !cls.Class.IsReadOnly() {
		// A non-read statement is blocked in read-only mode. Build a *core.Error
		// with code CodeSafety so handlers can branch on it; the hint and class
		// detail give the SPA enough context to explain the rejection.
		e := readOnlyRejection(cls)
		return cls.Statement, cls, e
	}

	out := cls.Statement
	if g.opts.AutoLimit > 0 && cls.Class.IsReadOnly() && limitInjectable(tokenize(cls.Statement)) && !cls.HasLimit {
		out = injectLimit(cls.Statement, g.opts.AutoLimit)
		cls.HasLimit = true
	}
	return out, cls, nil
}

// EnsureLimit returns sql with a top-level LIMIT cap injected when the statement
// is a limit-injectable read that lacks one. If auto-LIMIT is disabled (0) or the
// statement is not a bounded-injectable read, sql is returned unchanged.
func (g *Guard) EnsureLimit(sql string) string {
	if g.opts.AutoLimit <= 0 {
		return sql
	}
	stmt := trimStatement(sql)
	toks := tokenize(stmt)
	if !limitInjectable(toks) || hasTopLevelLimit(toks) {
		return sql
	}
	return injectLimit(stmt, g.opts.AutoLimit)
}

// readOnlyRejection builds the safety error returned by Check when a non-read
// statement is submitted in read-only mode. It carries code CodeSafety (via
// core.NewSafetyError) so the SPA can render the "this is not a read query"
// state, and names the offending class in both the message and required flags.
func readOnlyRejection(cls Classification) error {
	return core.NewSafetyError(
		"read-only query",
		[]string{"use a guided action for " + cls.Class.String() + " statements"},
		"statement classified as %s is not allowed in read-only mode; the query box only runs read statements",
		cls.Class,
	)
}

// DestructiveError signals a destructive operation that requires the operator to
// type the object's name to confirm. It embeds *core.SafetyError (code
// CodeSafety) and carries the exact string the operator must type.
type DestructiveError struct {
	*core.SafetyError
	Object string // the value the operator must type to confirm
}

// NewDestructiveError builds a DestructiveError for operation on object. The
// embedded SafetyError carries code CodeSafety and requires the typed-name
// confirmation flag (confirm=<object>).
func NewDestructiveError(object, operation string) *DestructiveError {
	se := core.NewSafetyError(
		operation,
		[]string{"confirm=" + object},
		"%s is destructive: type %q exactly to confirm", operation, object,
	)
	return &DestructiveError{SafetyError: se, Object: object}
}

// Unwrap returns the embedded *core.SafetyError so errors.As reaches it (and,
// through its own Unwrap, the underlying *core.Error). Without this, the chain
// would skip *core.SafetyError because the embedded type's own Unwrap returns
// the *core.Error directly. core.CodeOf still resolves CodeSafety either way.
//
// Error() is intentionally not redeclared: it promotes from *core.SafetyError.
func (e *DestructiveError) Unwrap() error { return e.SafetyError }

// --- helpers ---

// trimStatement trims surrounding whitespace and a single trailing semicolon
// (with any trailing whitespace) so classification and rewriting work on the
// bare statement.
func trimStatement(sql string) string {
	s := strings.TrimSpace(sql)
	for strings.HasSuffix(s, ";") {
		s = strings.TrimRight(s[:len(s)-1], " \t\n\r\f\v")
	}
	return s
}

// firstWords returns up to n leading word tokens (upper-cased), skipping
// non-word tokens such as a leading opening paren. Used to read leading verbs.
func firstWords(toks []token, n int) []string {
	out := make([]string, 0, n)
	for _, t := range toks {
		if t.kind == tokWord {
			out = append(out, t.upper())
			if len(out) == n {
				break
			}
		}
	}
	return out
}

// wordAt returns the upper-cased text of the word token at index i, or "" if i
// is out of range or not a word.
func wordAt(toks []token, i int) string {
	if i < 0 || i >= len(toks) {
		return ""
	}
	if toks[i].kind != tokWord {
		return ""
	}
	return toks[i].upper()
}

// limitInjectable reports whether a LIMIT can be appended at the top level of
// the statement. SELECT / TABLE / VALUES / WITH...SELECT accept a trailing
// LIMIT; SHOW / EXPLAIN do not. We only inject for plain selects.
func limitInjectable(toks []token) bool {
	lead := ""
	for _, t := range toks {
		if t.kind == tokWord {
			lead = t.upper()
			break
		}
	}
	switch lead {
	case "SELECT", "TABLE", "VALUES", "WITH":
		// For WITH, only the SELECT form is injectable; a data-modifying CTE is
		// not a read and never reaches here from Check, but EnsureLimit must also
		// guard it.
		return classifyWith(toks) == ClassRead || lead != "WITH"
	default:
		return false
	}
}

// hasTopLevelLimit reports whether a LIMIT clause appears at parenthesis depth 0.
// Top-level matters: LIMIT inside a subquery does not bound the outer result.
func hasTopLevelLimit(toks []token) bool {
	for _, t := range toks {
		if t.depth == 0 && t.isWord("LIMIT") {
			return true
		}
	}
	return false
}

// injectLimit appends a top-level LIMIT n to a statement known to lack one. The
// limit is placed before a trailing top-level FETCH if present (Postgres allows
// FETCH but injectLimit only runs when no LIMIT/FETCH bound exists). It returns
// the statement with the LIMIT appended.
func injectLimit(stmt string, n int) string {
	s := strings.TrimRight(stmt, " \t\n\r\f\v")
	return s + " LIMIT " + strconv.Itoa(n)
}
