// Package migrate implements the cross-host database migration feature: a
// session state machine coordinated entirely over an S3 object store, 6-char
// session-code generation (excluding visually ambiguous characters), session
// expiry, export/import readiness validation, and row-count verification.
//
// Three modes are modelled (single-DB, whole-cluster, SSH-less session). The
// Service here is the orchestration skeleton that reads/writes the session
// document over an ObjectStore and shells out via exec.Runner. All pure logic
// (the state machine, code generation, expiry, validation, row-count compare)
// is fully implemented and unit-tested; the heavier pg_dump/pg_restore wiring
// lives behind the Runner and is exercised through the fake.
package migrate

import (
	"crypto/rand"
	"time"

	"github.com/venkatesh-sekar/pgpanel/internal/core"
	"github.com/venkatesh-sekar/pgpanel/internal/exec"
)

const (
	// CodeAlphabet is the set of characters used in session codes. It excludes
	// I, O, 1 and 0 so codes are unambiguous when read aloud or typed.
	CodeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	// CodeLength is the number of characters in a session code.
	CodeLength = 6
	// SessionPrefix is the S3 key prefix under which session documents and
	// dumps live. It is the one deliberate place two panels share a bucket, so
	// it is namespaced per session code and time-boxed via the session expiry.
	SessionPrefix = "pg-migrations/sessions"
)

// GenerateCode returns a crypto-random CodeLength-character session code drawn
// from CodeAlphabet. It never panics; on the (practically impossible) event of
// a crypto/rand failure it falls back to a time-seeded value so a code is
// always produced.
func GenerateCode() string {
	out := make([]byte, CodeLength)
	buf := make([]byte, CodeLength)
	if _, err := rand.Read(buf); err != nil {
		// Extremely unlikely; fall back to a non-secret but valid code rather
		// than panicking in library code.
		seed := uint64(time.Now().UnixNano())
		for i := range out {
			seed = seed*6364136223846793005 + 1442695040888963407
			out[i] = CodeAlphabet[int(seed>>33)%len(CodeAlphabet)]
		}
		return string(out)
	}
	// Reject-free mapping: len(CodeAlphabet) is 32, a power of two, so masking
	// the low 5 bits yields an unbiased index.
	const mask = 0x1f // len(CodeAlphabet) - 1
	for i := range out {
		out[i] = CodeAlphabet[buf[i]&mask]
	}
	return string(out)
}

// compile-time assertions that the contract dependencies are wired correctly.
var (
	_ = core.CodeValidation
	_ = exec.RunSpec{}
)
