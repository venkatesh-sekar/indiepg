package core

// Confirmation carries the typed-name confirmation a caller must supply to
// proceed with a destructive operation. RequireConfirmation compares the
// operator-supplied Typed value against the Expected object name.
type Confirmation struct {
	// Expected is the exact value the operator must type (usually the object
	// name, e.g. the database or role being dropped).
	Expected string
	// Typed is what the operator actually supplied.
	Typed string
}

// OK reports whether the confirmation matches exactly.
func (c Confirmation) OK() bool {
	return c.Expected != "" && c.Typed == c.Expected
}

// RequireConfirmation returns a *SafetyError unless typed exactly equals
// expected. operation names the action for the error message (e.g.
// "drop database orders"). On success it returns nil.
func RequireConfirmation(operation, expected, typed string) *SafetyError {
	c := Confirmation{Expected: expected, Typed: typed}
	if c.OK() {
		return nil
	}
	return NewSafetyError(
		operation,
		[]string{"confirm=" + expected},
		"%s requires typing %q to confirm", operation, expected,
	)
}

// Result is a small, serializable outcome wrapper for guided actions. Handlers
// return it so the SPA can render a uniform success/explanation surface. The
// zero value is a non-OK result.
type Result struct {
	OK      bool           `json:"ok"`
	Message string         `json:"message,omitempty"`
	Data    map[string]any `json:"data,omitempty"`
	// Statements optionally records the SQL/commands that were (or would be)
	// executed, for the audit log and dry-run display.
	Statements []string `json:"statements,omitempty"`
}

// Ok builds a successful Result with a message.
func Ok(message string) Result {
	return Result{OK: true, Message: message}
}

// WithData returns a copy of the Result with key=value added to Data.
func (r Result) WithData(key string, value any) Result {
	out := r
	out.Data = make(map[string]any, len(r.Data)+1)
	for k, v := range r.Data {
		out.Data[k] = v
	}
	out.Data[key] = value
	return out
}

// WithStatements returns a copy of the Result recording the given statements.
func (r Result) WithStatements(stmts ...string) Result {
	out := r
	out.Statements = append(append([]string(nil), r.Statements...), stmts...)
	return out
}
