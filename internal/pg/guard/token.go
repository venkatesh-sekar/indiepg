package guard

import "strings"

// tokenKind enumerates the lexical categories the guard's tiny SQL scanner
// distinguishes. It is deliberately coarse: the guard only needs to find
// leading keywords, top-level clauses, and whether a statement is bounded, not
// to fully parse SQL.
type tokenKind int

const (
	tokWord   tokenKind = iota // a bare word / keyword / identifier (unquoted)
	tokQuoted                  // a double-quoted identifier ("My Table")
	tokString                  // a single-quoted or dollar-quoted string literal
	tokNumber                  // a numeric literal
	tokPunct                   // a single punctuation rune ( ) , ; . etc.
	tokParam                   // a bind placeholder ($1, $2, ...)
)

// token is one lexical unit with its nesting depth. depth is the parenthesis
// nesting level at the START of the token (0 == top level). Tracking depth lets
// the guard reason about *top-level* clauses (e.g. a top-level LIMIT) while
// ignoring identically-named tokens inside subqueries or function calls.
type token struct {
	kind  tokenKind
	text  string // original text (for words: as written; callers lowercase as needed)
	depth int
}

// upper returns the token text upper-cased, for case-insensitive keyword
// comparisons. Only meaningful for tokWord.
func (t token) upper() string { return strings.ToUpper(t.text) }

// isWord reports whether the token is an unquoted word equal (case-insensitively)
// to kw.
func (t token) isWord(kw string) bool {
	return t.kind == tokWord && strings.EqualFold(t.text, kw)
}

// tokenize scans a single SQL statement into tokens, stripping comments and
// collapsing string/comment content. It is intentionally forgiving: malformed
// input never panics, it simply yields whatever tokens it can. Parenthesis
// depth is tracked so callers can find top-level clauses.
//
// Handled lexical features (matching PostgreSQL):
//   - line comments  -- ... to end of line
//   - block comments /* ... */ (nesting-aware, PostgreSQL allows nesting)
//   - single-quoted strings with ” escaping
//   - E” escape strings with backslash escaping
//   - dollar-quoted strings $tag$ ... $tag$
//   - double-quoted identifiers with "" escaping
//   - bind parameters $1, $2
func tokenize(sql string) []token {
	var out []token
	depth := 0
	i := 0
	n := len(sql)

	for i < n {
		c := sql[i]

		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v':
			i++

		case c == '-' && i+1 < n && sql[i+1] == '-':
			// line comment
			i += 2
			for i < n && sql[i] != '\n' {
				i++
			}

		case c == '/' && i+1 < n && sql[i+1] == '*':
			// block comment, nesting-aware
			i += 2
			nest := 1
			for i < n && nest > 0 {
				if i+1 < n && sql[i] == '/' && sql[i+1] == '*' {
					nest++
					i += 2
				} else if i+1 < n && sql[i] == '*' && sql[i+1] == '/' {
					nest--
					i += 2
				} else {
					i++
				}
			}

		case c == '\'':
			// standard single-quoted string with '' escaping
			i = scanSingleQuoted(sql, i+1)
			out = append(out, token{kind: tokString, depth: depth})

		case (c == 'E' || c == 'e') && i+1 < n && sql[i+1] == '\'':
			// escape string E'...'
			i = scanEscapeString(sql, i+2)
			out = append(out, token{kind: tokString, depth: depth})

		case c == '$':
			// either a dollar-quoted string ($tag$...$tag$) or a bind param ($1)
			if tag, end, ok := scanDollarTag(sql, i); ok {
				closeTag := tag
				rest := sql[end:]
				idx := strings.Index(rest, closeTag)
				if idx < 0 {
					// unterminated; consume to end
					i = n
				} else {
					i = end + idx + len(closeTag)
				}
				out = append(out, token{kind: tokString, depth: depth})
			} else if i+1 < n && isDigit(sql[i+1]) {
				j := i + 1
				for j < n && isDigit(sql[j]) {
					j++
				}
				out = append(out, token{kind: tokParam, text: sql[i:j], depth: depth})
				i = j
			} else {
				out = append(out, token{kind: tokPunct, text: "$", depth: depth})
				i++
			}

		case c == '"':
			// double-quoted identifier with "" escaping
			j := i + 1
			var b strings.Builder
			for j < n {
				if sql[j] == '"' {
					if j+1 < n && sql[j+1] == '"' {
						b.WriteByte('"')
						j += 2
						continue
					}
					j++
					break
				}
				b.WriteByte(sql[j])
				j++
			}
			out = append(out, token{kind: tokQuoted, text: b.String(), depth: depth})
			i = j

		case c == '(':
			out = append(out, token{kind: tokPunct, text: "(", depth: depth})
			depth++
			i++

		case c == ')':
			if depth > 0 {
				depth--
			}
			out = append(out, token{kind: tokPunct, text: ")", depth: depth})
			i++

		case isDigit(c):
			j := i
			for j < n && (isDigit(sql[j]) || sql[j] == '.' || sql[j] == 'e' || sql[j] == 'E' ||
				((sql[j] == '+' || sql[j] == '-') && j > i && (sql[j-1] == 'e' || sql[j-1] == 'E'))) {
				j++
			}
			out = append(out, token{kind: tokNumber, text: sql[i:j], depth: depth})
			i = j

		case isWordStart(c):
			j := i
			for j < n && isWordPart(sql[j]) {
				j++
			}
			out = append(out, token{kind: tokWord, text: sql[i:j], depth: depth})
			i = j

		default:
			out = append(out, token{kind: tokPunct, text: string(c), depth: depth})
			i++
		}
	}
	return out
}

// scanSingleQuoted returns the index just past a single-quoted string whose
// opening quote was already consumed; start points at the first content byte.
func scanSingleQuoted(sql string, start int) int {
	n := len(sql)
	i := start
	for i < n {
		if sql[i] == '\'' {
			if i+1 < n && sql[i+1] == '\'' {
				i += 2
				continue
			}
			return i + 1
		}
		i++
	}
	return n
}

// scanEscapeString returns the index just past an E'...' escape string; start
// points at the first content byte (after E'). Backslash escapes any next byte.
func scanEscapeString(sql string, start int) int {
	n := len(sql)
	i := start
	for i < n {
		switch sql[i] {
		case '\\':
			i += 2
		case '\'':
			if i+1 < n && sql[i+1] == '\'' {
				i += 2
				continue
			}
			return i + 1
		default:
			i++
		}
	}
	return n
}

// scanDollarTag, given that sql[pos]=='$', tries to read a dollar-quote opening
// tag $tag$ (tag may be empty: $$). On success it returns the full tag text
// (including both $), the index just past the closing $, and true.
func scanDollarTag(sql string, pos int) (tag string, end int, ok bool) {
	n := len(sql)
	if pos >= n || sql[pos] != '$' {
		return "", 0, false
	}
	j := pos + 1
	for j < n && sql[j] != '$' {
		c := sql[j]
		if !(c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (j > pos+1 && c >= '0' && c <= '9')) {
			return "", 0, false
		}
		j++
	}
	if j >= n || sql[j] != '$' {
		return "", 0, false
	}
	return sql[pos : j+1], j + 1, true
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isWordStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c >= 0x80
}

func isWordPart(c byte) bool {
	return c == '_' || c == '$' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9') || c >= 0x80
}
