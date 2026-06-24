package backup

import (
	"strings"
	"time"

	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/exec"
)

// Command timeouts. Info is quick; backup/restore can be long-running. verify
// reads every backup and WAL file in the repo to checksum it, so it can run long
// on a large repo, but it never restores — far cheaper than a real restore.
const (
	infoTimeout    = 60 * time.Second
	backupTimeout  = 6 * time.Hour
	restoreTimeout = 6 * time.Hour
	verifyTimeout  = 2 * time.Hour
)

// RecoveryTarget describes a point-in-time-recovery target for a restore.
// Exactly one of Time/XID/LSN/Name selects where recovery stops; a zero-value
// target (all fields empty) means "recover to the latest available WAL".
type RecoveryTarget struct {
	Time   *time.Time // recover to this timestamp
	XID    string     // recover to this transaction id
	LSN    string     // recover to this log sequence number
	Name   string     // recover to this named restore point
	Action string     // promote|pause|shutdown (post-recovery action)
}

// IsZero reports whether the target selects nothing (recover to latest).
func (t RecoveryTarget) IsZero() bool {
	return t.Time == nil && t.XID == "" && t.LSN == "" && t.Name == ""
}

// Validate ensures at most one recovery target is set and the action, if given,
// is a recognized pgBackRest target action. It returns *core.Error
// (CodeValidation) on a conflict.
func (t RecoveryTarget) Validate() error {
	n := 0
	if t.Time != nil {
		n++
	}
	if t.XID != "" {
		n++
	}
	if t.LSN != "" {
		n++
	}
	if t.Name != "" {
		n++
	}
	if n > 1 {
		return core.ValidationError("recovery target must set exactly one of time/xid/lsn/name")
	}
	switch t.Action {
	case "", "promote", "pause", "shutdown":
	default:
		return core.ValidationError("invalid recovery action %q (want promote|pause|shutdown)", t.Action)
	}
	return t.validateContent()
}

// validateContent rejects a malformed XID/LSN/Name up front. Each is joined into
// a single `--target=<value>` argv token (no shell, value position), so this is
// not an injection boundary — it is defense-in-depth plus a clear, fail-fast error
// for the operator instead of an opaque pgBackRest failure partway through a
// restore (the most data-critical moment to get a confusing error). At most one of
// the three is set here (the n>1 conflict is rejected above).
func (t RecoveryTarget) validateContent() error {
	switch {
	case t.XID != "":
		// A transaction id is a non-negative integer. 64-bit xid8 is at most
		// 20 digits; cap there so a junk value can't be unbounded.
		if len(t.XID) > 20 || !isAllDigits(t.XID) {
			return core.ValidationError("invalid recovery xid %q (want a numeric transaction id)", t.XID)
		}
	case t.LSN != "":
		if !isValidLSN(t.LSN) {
			return core.ValidationError("invalid recovery lsn %q (want hex form like 0/16B6A50)", t.LSN)
		}
	case t.Name != "":
		// A named restore point can contain spaces and printable Unicode, but
		// never control characters or line/paragraph separators.
		if len(t.Name) > 128 {
			return core.ValidationError("recovery target name too long (max 128 bytes)")
		}
		for _, r := range t.Name {
			if r < 0x20 || r == 0x7f || r == ' ' || r == ' ' {
				return core.ValidationError("invalid recovery target name %q (control characters not allowed)", t.Name)
			}
		}
	}
	return nil
}

// isAllDigits reports whether s is a non-empty run of ASCII digits.
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// isValidLSN reports whether s is a Postgres log sequence number: two non-empty
// hex runs joined by exactly one slash (e.g. "0/16B6A50"). A Postgres LSN is a
// 64-bit value rendered as two 32-bit halves, so each hex run is at most 8 digits.
func isValidLSN(s string) bool {
	hi, lo, ok := strings.Cut(s, "/")
	if !ok {
		return false
	}
	if strings.IndexByte(lo, '/') >= 0 {
		return false // more than one slash
	}
	return len(hi) > 0 && len(hi) <= 8 && len(lo) > 0 && len(lo) <= 8 && isHex(hi) && isHex(lo)
}

// isHex reports whether s is a run of ASCII hex digits.
func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// targetArgs renders the pgBackRest --target* flags for this recovery target.
// The caller must have validated the target first.
func (t RecoveryTarget) targetArgs() []string {
	var args []string
	switch {
	case t.Time != nil:
		args = append(args,
			"--type=time",
			// pgBackRest expects "YYYY-MM-DD HH:MM:SS+TZ".
			"--target="+t.Time.UTC().Format("2006-01-02 15:04:05-07"),
		)
	case t.XID != "":
		args = append(args, "--type=xid", "--target="+t.XID)
	case t.LSN != "":
		args = append(args, "--type=lsn", "--target="+t.LSN)
	case t.Name != "":
		args = append(args, "--type=name", "--target="+t.Name)
	}
	if t.Action != "" {
		args = append(args, "--target-action="+t.Action)
	}
	return args
}

// InfoCmd builds the `pgbackrest info --output=json` invocation for a stanza.
func InfoCmd(stanza string) exec.RunSpec {
	return exec.RunSpec{
		Name:    pgbackrestBin,
		Args:    []string{"--stanza=" + stanza, "--output=json", "info"},
		AsUser:  pgUser,
		Timeout: infoTimeout,
	}
}

// VerifyCmd builds the `pgbackrest verify` invocation for a stanza. verify is a
// read-only repository integrity check: it confirms every backup and WAL file is
// present and matches its recorded checksum/size. It never restores and never
// touches the live data directory.
func VerifyCmd(stanza string) exec.RunSpec {
	return exec.RunSpec{
		Name:    pgbackrestBin,
		Args:    []string{"--stanza=" + stanza, "verify"},
		AsUser:  pgUser,
		Timeout: verifyTimeout,
	}
}

// BackupCmd builds the `pgbackrest backup` invocation for a stanza and type.
func BackupCmd(stanza string, t Type) exec.RunSpec {
	return exec.RunSpec{
		Name:    pgbackrestBin,
		Args:    []string{"--stanza=" + stanza, "--type=" + string(t), "backup"},
		AsUser:  pgUser,
		Timeout: backupTimeout,
	}
}

// RestoreCmd builds the `pgbackrest restore` invocation for a stanza, optional
// PITR target, and delta mode. It validates the target and returns *core.Error
// on a conflicting target.
//
// delta restores only the files that differ from the existing data dir (faster,
// in-place); without it pgBackRest expects an empty target directory.
func RestoreCmd(stanza string, target *RecoveryTarget, delta bool) (exec.RunSpec, error) {
	if err := validateStanza(stanza); err != nil {
		return exec.RunSpec{}, err
	}
	args := []string{"--stanza=" + stanza}
	if delta {
		args = append(args, "--delta")
	}
	if target != nil {
		if err := target.Validate(); err != nil {
			return exec.RunSpec{}, err
		}
		args = append(args, target.targetArgs()...)
	}
	args = append(args, "restore")
	return exec.RunSpec{
		Name:    pgbackrestBin,
		Args:    args,
		AsUser:  pgUser,
		Timeout: restoreTimeout,
	}, nil
}
