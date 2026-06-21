package server

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/venkatesh-sekar/indiepg/internal/core"
)

// genericInternalMessage is the safe, fixed client-facing message used whenever
// an error is not a typed panel error. Raw error text is never placed in the
// wire payload for these; it is kept to server-side logs only so an unexpected
// internal failure cannot leak connection strings, SQL fragments, file paths,
// or other sensitive detail to the SPA.
const genericInternalMessage = "internal server error"

// apiError is the JSON error envelope returned for any failed request. The
// stable Code lets the SPA branch on the kind of failure (e.g. render a
// typed-name confirm dialog for a safety error, or a login form for an auth
// error) without string-matching messages.
type apiError struct {
	Code    core.Code      `json:"code"`
	Message string         `json:"message"`
	Hint    string         `json:"hint,omitempty"`
	Details map[string]any `json:"details,omitempty"`
	// Operation and RequiredFlags are populated for safety errors so the SPA
	// knows what confirmation is required to proceed.
	Operation     string   `json:"operation,omitempty"`
	RequiredFlags []string `json:"required_flags,omitempty"`
	// Ownership fields are populated for ownership conflicts (HARD STOP) so the
	// SPA can render the actionable "owned by panel X" message and an Adopt
	// affordance when the owner looks abandoned.
	Owner *ownerDetail `json:"owner,omitempty"`
}

// ownerDetail mirrors the actionable fields of a core.OwnershipError.
type ownerDetail struct {
	OwnerID   string `json:"owner_id"`
	OwnerHost string `json:"owner_host"`
	LastSeen  string `json:"last_seen"`
	Adoptable bool   `json:"adoptable"`
}

// envelope wraps a successful payload under {"data": ...} so the SPA reads a
// consistent shape across endpoints.
type envelope struct {
	Data any `json:"data,omitempty"`
}

// statusForCode maps a stable core.Code to an HTTP status. Unknown codes and
// internal errors map to 500; this is the single place HTTP status is decided.
func statusForCode(code core.Code) int {
	switch code {
	case core.CodeValidation:
		return http.StatusBadRequest
	case core.CodeAuth:
		return http.StatusUnauthorized
	case core.CodeLocked:
		return http.StatusTooManyRequests
	case core.CodeSafety, core.CodeOwnership:
		return http.StatusConflict
	case core.CodeNotFound:
		return http.StatusNotFound
	case core.CodeConflict:
		return http.StatusConflict
	case core.CodeExec, core.CodeInternal:
		return http.StatusInternalServerError
	case "":
		return http.StatusOK
	default:
		return http.StatusInternalServerError
	}
}

// writeJSON serializes v as JSON with the given status. Encoding failures are
// logged by the caller's recovery path; here we best-effort write a 500.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	enc := json.NewEncoder(w)
	_ = enc.Encode(v)
}

// writeData writes a 2xx JSON success envelope.
func writeData(w http.ResponseWriter, status int, data any) {
	writeJSON(w, status, envelope{Data: data})
}

// writeError renders any error as the JSON apiError envelope with the HTTP
// status derived from its stable code. Non-panel errors are normalized to a
// generic internal error and their raw text never reaches the client payload.
func writeError(w http.ResponseWriter, err error) {
	ae, status := toAPIError(err)
	writeJSON(w, status, ae)
}

// toAPIError converts any error into the wire shape plus its HTTP status. It
// understands the three typed error shapes from core (Error, SafetyError,
// OwnershipError) and extracts their actionable fields.
func toAPIError(err error) (apiError, int) {
	if err == nil {
		return apiError{Code: core.CodeInternal, Message: "nil error"}, http.StatusInternalServerError
	}

	code := core.CodeOf(err)

	// Safe-by-default: the client Message is only ever built from a typed panel
	// error's curated Message. For any other error the payload carries a fixed
	// generic string and the raw text is confined to the server-side log, so an
	// unexpected internal failure cannot leak sensitive detail to the SPA.
	out := apiError{Code: code, Message: genericInternalMessage}

	if pe, ok := core.AsError(err); ok {
		out.Message = pe.Message
		out.Hint = pe.Hint
		out.Details = pe.Details
	} else {
		slog.Error("non-panel error normalized to internal", "err", err.Error())
	}

	var se *core.SafetyError
	if errors.As(err, &se) {
		out.Operation = se.Operation
		out.RequiredFlags = se.RequiredFlags
	}

	var oe *core.OwnershipError
	if errors.As(err, &oe) {
		out.Owner = &ownerDetail{
			OwnerID:   oe.OwnerID,
			OwnerHost: oe.OwnerHost,
			LastSeen:  oe.LastSeen,
			Adoptable: oe.Adoptable,
		}
	}

	return out, statusForCode(code)
}

// decodeJSON reads and strictly decodes a JSON request body into dst, rejecting
// unknown fields and oversized bodies. It returns a CodeValidation error on
// malformed input.
func decodeJSON(r *http.Request, dst any, maxBytes int64) error {
	if r.Body == nil {
		return core.ValidationError("empty request body")
	}
	r.Body = http.MaxBytesReader(nil, r.Body, maxBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return core.ValidationError("invalid JSON body").Wrap(err)
	}
	return nil
}
