// Package backup wraps pgBackRest for the panel: it builds pgBackRest command
// invocations (info/backup/restore) as exec.RunSpecs, parses the `pgbackrest
// info --output=json` document into a BackupInfo stats model, and orchestrates
// backup/restore through an exec.Runner.
//
// Every repo-writing operation is gated by single-writer ownership enforced via
// internal/identity: the Manager claims/verifies the repo prefix and heartbeats
// before pgBackRest touches the repository. A foreign, non-stale owner is a HARD
// STOP (*core.OwnershipError) — two panels must never share a backup repo.
//
// Nothing here performs IO except through the injected exec.Runner, the
// *store.Store, and the *identity.Owner. All command building and JSON parsing
// is pure and unit-tested without a live Postgres, pgBackRest, or network.
package backup

import (
	"github.com/venkatesh-sekar/pgpanel/internal/core"
)

// Type is the pgBackRest backup type.
type Type string

const (
	// TypeFull is a full backup (copies the entire database cluster).
	TypeFull Type = "full"
	// TypeDiff is a differential backup (changes since the last full).
	TypeDiff Type = "diff"
	// TypeIncr is an incremental backup (changes since the last backup).
	TypeIncr Type = "incr"
)

// String implements fmt.Stringer.
func (t Type) String() string { return string(t) }

// ParseType normalizes s into a Type, returning *core.Error (CodeValidation)
// for an unknown value. Common aliases ("incremental", "differential") are
// accepted.
func ParseType(s string) (Type, error) {
	switch s {
	case string(TypeFull):
		return TypeFull, nil
	case string(TypeDiff), "differential":
		return TypeDiff, nil
	case string(TypeIncr), "incremental":
		return TypeIncr, nil
	default:
		return "", core.ValidationError("unknown backup type %q (want full|diff|incr)", s)
	}
}

// pgbackrestBin is the pgBackRest executable name (resolved on PATH).
const pgbackrestBin = "pgbackrest"

// pgUser is the OS user pgBackRest runs as (it must own the Postgres data dir
// and the repo configuration). Backups never run as root.
const pgUser = "postgres"

// validateStanza guards against shell/config injection through the stanza name.
// pgBackRest stanza names are limited to lowercase letters, digits, and dashes.
func validateStanza(stanza string) error {
	if stanza == "" {
		return core.ValidationError("stanza name is required")
	}
	if len(stanza) > 128 {
		return core.ValidationError("stanza name too long")
	}
	for _, r := range stanza {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		return core.ValidationError("invalid stanza name %q (want lowercase letters, digits, dashes)", stanza)
	}
	return nil
}
