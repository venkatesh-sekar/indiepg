// Package core holds shared primitives used by every pgpanel package: typed
// errors with stable codes, a confirmation/result helper, identifier
// validation and safe SQL quoting, and a small structured logger.
//
// Nothing in this package performs external side effects. It never panics in
// library paths; callers receive typed errors instead.
package core

import (
	"errors"
	"fmt"
)

// Code is a stable, machine-readable error code carried by every panel error.
// The JSON API surfaces it so the SPA can branch on the kind of failure
// (e.g. show a typed-name confirm dialog for CodeSafety).
type Code string

const (
	CodeValidation Code = "validation" // bad input (identifier, port, url, ...)
	CodeSafety     Code = "safety"     // a guard rail blocked a risky operation
	CodeOwnership  Code = "ownership"  // foreign single-writer owner / claim conflict
	CodeNotFound   Code = "not_found"  // requested resource does not exist
	CodeConflict   Code = "conflict"   // resource already exists / state conflict
	CodeExec       Code = "exec"       // an external command failed
	CodeAuth       Code = "auth"       // authentication / session failure
	CodeLocked     Code = "locked"     // account locked out
	CodeInternal   Code = "internal"   // unexpected internal error
)

// Error is the common typed error for the whole panel. Every domain error
// embeds it (via the constructors below) so handlers can uniformly read a
// stable Code, a human Message, an optional Hint, and structured Details.
//
// Error implements error and supports errors.Is/errors.As and %w wrapping
// through the wrapped cause.
type Error struct {
	Code    Code           // stable machine-readable code
	Message string         // human-readable description
	Hint    string         // optional suggested remediation
	Details map[string]any // optional structured context (safe to serialize)
	cause   error          // wrapped underlying error, if any
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Unwrap returns the wrapped cause so errors.Is/As traverse the chain.
func (e *Error) Unwrap() error { return e.cause }

// Is reports equality by Code, so errors.Is(err, &Error{Code: CodeNotFound})
// matches any panel error with that code regardless of message.
func (e *Error) Is(target error) bool {
	var t *Error
	if errors.As(target, &t) {
		return t.Code == "" || e.Code == t.Code
	}
	return false
}

// WithHint returns a copy of the error with the hint set.
func (e *Error) WithHint(hint string) *Error {
	cp := *e
	cp.Hint = hint
	return &cp
}

// WithDetail returns a copy of the error with one structured detail added.
func (e *Error) WithDetail(key string, value any) *Error {
	cp := *e
	cp.Details = make(map[string]any, len(e.Details)+1)
	for k, v := range e.Details {
		cp.Details[k] = v
	}
	cp.Details[key] = value
	return &cp
}

// Wrap returns a copy of the error wrapping cause, so %w and errors.Is/As
// reach the underlying error.
func (e *Error) Wrap(cause error) *Error {
	cp := *e
	cp.cause = cause
	return &cp
}

// newError builds a *Error, formatting message with fmt.Sprintf semantics.
func newError(code Code, format string, args ...any) *Error {
	msg := format
	if len(args) > 0 {
		msg = fmt.Sprintf(format, args...)
	}
	return &Error{Code: code, Message: msg}
}

// ValidationError reports invalid input (identifiers, ports, URLs, paths).
func ValidationError(format string, args ...any) *Error {
	return newError(CodeValidation, format, args...)
}

// NotFoundError reports a missing resource.
func NotFoundError(format string, args ...any) *Error {
	return newError(CodeNotFound, format, args...)
}

// ConflictError reports a state/existence conflict.
func ConflictError(format string, args ...any) *Error {
	return newError(CodeConflict, format, args...)
}

// ExecError reports a failed external command.
func ExecError(format string, args ...any) *Error {
	return newError(CodeExec, format, args...)
}

// AuthError reports an authentication or session failure.
func AuthError(format string, args ...any) *Error {
	return newError(CodeAuth, format, args...)
}

// LockedError reports a locked-out account.
func LockedError(format string, args ...any) *Error {
	return newError(CodeLocked, format, args...)
}

// InternalError reports an unexpected internal failure.
func InternalError(format string, args ...any) *Error {
	return newError(CodeInternal, format, args...)
}

// SafetyError reports that a guard rail blocked a risky operation. Operation
// names the blocked action; RequiredFlags lists what the caller must supply
// (e.g. a typed-name confirm) to proceed.
type SafetyError struct {
	Err           *Error
	Operation     string   // the blocked operation, e.g. "drop database"
	RequiredFlags []string // what is needed to proceed
}

func (e *SafetyError) Error() string { return e.Err.Error() }
func (e *SafetyError) Unwrap() error { return e.Err }

// NewSafetyError builds a SafetyError with code CodeSafety.
func NewSafetyError(operation string, requiredFlags []string, format string, args ...any) *SafetyError {
	return &SafetyError{
		Err:           newError(CodeSafety, format, args...),
		Operation:     operation,
		RequiredFlags: requiredFlags,
	}
}

// OwnershipError reports a single-writer ownership conflict on a shared
// resource (e.g. an S3 backup repo owned by another panel). It is a HARD STOP:
// callers must never proceed past it into corruption.
type OwnershipError struct {
	Err       *Error
	Resource  string // the shared resource, e.g. "s3://bucket/panel/<id>"
	OwnerID   string // instance_id of the current owner
	OwnerHost string // hostname of the current owner
	LastSeen  string // RFC3339 timestamp of the owner's last heartbeat
	Adoptable bool   // true if the owner looks abandoned and may be adopted
}

func (e *OwnershipError) Error() string { return e.Err.Error() }
func (e *OwnershipError) Unwrap() error { return e.Err }

// NewOwnershipError builds an OwnershipError with code CodeOwnership.
func NewOwnershipError(resource, ownerID, ownerHost, lastSeen string, adoptable bool, format string, args ...any) *OwnershipError {
	return &OwnershipError{
		Err:       newError(CodeOwnership, format, args...),
		Resource:  resource,
		OwnerID:   ownerID,
		OwnerHost: ownerHost,
		LastSeen:  lastSeen,
		Adoptable: adoptable,
	}
}

// CodeOf extracts the stable Code from any error in the chain, defaulting to
// CodeInternal for non-panel errors and "" for nil.
func CodeOf(err error) Code {
	if err == nil {
		return ""
	}
	var pe *Error
	if errors.As(err, &pe) {
		return pe.Code
	}
	return CodeInternal
}

// AsError extracts the underlying *Error from any error in the chain.
func AsError(err error) (*Error, bool) {
	var pe *Error
	if errors.As(err, &pe) {
		return pe, true
	}
	return nil, false
}
