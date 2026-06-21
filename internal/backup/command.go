package backup

import (
	"time"

	"github.com/venkatesh-sekar/pgpanel/internal/core"
	"github.com/venkatesh-sekar/pgpanel/internal/exec"
)

// Command timeouts. Info is quick; backup/restore can be long-running.
const (
	infoTimeout    = 60 * time.Second
	backupTimeout  = 6 * time.Hour
	restoreTimeout = 6 * time.Hour
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
	return nil
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
