package pg

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/exec"
)

// livenessTimeout bounds the post-restart probe. A down postmaster refuses the
// socket connection immediately, so this only caps the rare up-but-hung case.
const livenessTimeout = 30 * time.Second

// autoConfFileName is the file ALTER SYSTEM writes to, inside the data
// directory. Snapshotting and restoring this file is how a config change that
// stops Postgres is rolled back to last-known-good: ALTER SYSTEM RESET needs a
// running server, but a file restore works even while Postgres is down — which
// is exactly the situation a bad postmaster setting leaves us in.
const autoConfFileName = "postgresql.auto.conf"

// autoConfSnapshot is a captured postgresql.auto.conf, used to roll a failed
// config change back to exactly the prior state.
type autoConfSnapshot struct {
	path    string
	content string
}

// snapshotAutoConf captures the current postgresql.auto.conf so a later config
// change can be reverted to precisely this state. It fails closed: if the file
// cannot be read, the caller must NOT proceed with a restart it would be unable
// to undo. The file (mode 0600, owned by postgres) is read as the postgres OS
// user over peer auth.
func (m *Manager) snapshotAutoConf(ctx context.Context) (autoConfSnapshot, error) {
	if m.runner == nil {
		return autoConfSnapshot{}, core.InternalError("pg: snapshotAutoConf requires a Runner")
	}
	dir, err := m.DataDirectory(ctx)
	if err != nil {
		return autoConfSnapshot{}, err
	}
	path := filepath.Join(dir, autoConfFileName)
	res, err := m.runner.Run(ctx, exec.RunSpec{
		Name:    "cat",
		AsUser:  "postgres",
		Args:    []string{"--", path},
		Timeout: commandTimeout,
	})
	if err != nil {
		return autoConfSnapshot{}, core.ExecError("pg: snapshotting %s for rollback failed", autoConfFileName).Wrap(err)
	}
	return autoConfSnapshot{path: path, content: res.Stdout}, nil
}

// restoreAutoConf writes a snapshot's content back to postgresql.auto.conf,
// truncating any failed change. Written as the postgres OS user via tee so the
// file keeps its postgres owner and 0600 mode (a fresh redirect would not).
func (m *Manager) restoreAutoConf(ctx context.Context, snap autoConfSnapshot) error {
	_, err := m.runner.Run(ctx, exec.RunSpec{
		Name:    "tee",
		AsUser:  "postgres",
		Args:    []string{snap.path},
		Stdin:   snap.content,
		Timeout: commandTimeout,
	})
	if err != nil {
		return core.ExecError("pg: restoring %s during rollback failed", autoConfFileName).Wrap(err)
	}
	return nil
}

// restartWithRollback restarts Postgres to activate a config change and
// self-heals if that change prevents startup. snap must have been captured
// BEFORE the change was written. `what` names the change for the operator
// (e.g. "WAL archiving config").
//
// Health is judged by a real liveness probe (see restartAndConfirm), NOT the
// `systemctl restart` exit code: on Debian/Ubuntu — exactly the platform we
// provision via apt — the `postgresql` unit is a oneshot wrapper that pulls in
// the real postgresql@<ver>-main.service via a non-binding Wants, so the restart
// exits 0 even when the cluster fails to start. If the new config does not come
// up, this restores snap (last-known-good) and restarts again, so the cluster is
// never left down, then returns a CodeSafety error naming the rejected change.
// If Postgres is still down after the rollback restart, it returns a
// CodeInternal error making clear Postgres is down.
func (m *Manager) restartWithRollback(ctx context.Context, snap autoConfSnapshot, what string) error {
	if err := m.restartAndConfirm(ctx); err == nil {
		return nil // Postgres restarted cleanly and is accepting connections
	} else {
		m.log.Error("config change prevented Postgres from coming back up; rolling back to last-known-good",
			"change", what, "error", err.Error())
	}

	if err := m.restoreAutoConf(ctx, snap); err != nil {
		return core.InternalError(
			"pg: %s prevented Postgres from starting and the rollback could not restore last-known-good config; Postgres is down", what).
			WithHint("postgresql.auto.conf still contains the rejected settings; restore it manually before restarting Postgres").
			Wrap(err)
	}
	if err := m.restartAndConfirm(ctx); err != nil {
		return core.InternalError(
			"pg: %s prevented Postgres from starting and the rollback restart also failed; Postgres is down", what).Wrap(err)
	}

	m.log.InfoCtx(ctx, "rolled back failed config change; Postgres restarted on last-known-good", "change", what)
	return &core.Error{
		Code:    core.CodeSafety,
		Message: fmt.Sprintf("the %s change prevented Postgres from starting and was automatically rolled back to last-known-good; Postgres is running", what),
		Hint:    "Review the change before re-applying; the previous working configuration is in effect.",
	}
}

// restartAndConfirm restarts the managed Postgres unit and verifies the
// postmaster actually came back up accepting connections. It returns nil ONLY
// when both the restart succeeded AND the liveness probe confirms Postgres is
// live — never trusting the systemd wrapper unit's exit code alone (see
// confirmAcceptingConnections).
func (m *Manager) restartAndConfirm(ctx context.Context) error {
	if err := m.restartService(ctx); err != nil {
		return err
	}
	return m.confirmAcceptingConnections(ctx)
}

// restartService restarts the managed Postgres systemd unit.
func (m *Manager) restartService(ctx context.Context) error {
	_, err := m.runner.Run(ctx, exec.RunSpec{
		Name:    "systemctl",
		Args:    []string{"restart", serviceName},
		Timeout: commandTimeout,
	})
	return err
}

// confirmAcceptingConnections probes the local postmaster directly with a real
// `SELECT 1` over the unix socket (as the postgres OS user, peer auth). This —
// NOT the `systemctl restart` exit code — is the authoritative "did Postgres
// come back up?" signal, because the Debian/Ubuntu `postgresql` wrapper unit
// exits 0 regardless of whether the real cluster started. A bounded timeout
// keeps a hung postmaster from blocking the rollback. SELECT 1 carries no
// secret, so no log redaction is needed.
func (m *Manager) confirmAcceptingConnections(ctx context.Context) error {
	if _, err := m.runner.Run(ctx, exec.RunSpec{
		Name:    "psql",
		AsUser:  "postgres",
		Args:    []string{"-v", "ON_ERROR_STOP=1", "-tAqX", "-d", defaultConnectDatabase, "-c", "SELECT 1"},
		Timeout: livenessTimeout,
	}); err != nil {
		return core.ExecError("pg: Postgres did not accept connections after restart").Wrap(err)
	}
	return nil
}
