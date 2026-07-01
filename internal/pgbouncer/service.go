package pgbouncer

import (
	"context"
	"strings"
	"time"

	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/exec"
)

// serviceName is the systemd unit for the managed PgBouncer (the Debian/Ubuntu
// package ships a unit of this name).
const serviceName = "pgbouncer"

// pkgName is the apt package providing PgBouncer.
const pkgName = "pgbouncer"

// commandTimeout bounds an individual apt/systemctl command so a wedged install
// or a hung unit transition cannot block the panel forever. It matches the pg
// manager's provisioning timeout.
const commandTimeout = 10 * time.Minute

// InstallPackage installs the PgBouncer package via apt. It refreshes the package
// index first, then installs; both steps are idempotent — an already-installed
// package is a no-op ("pgbouncer is already the newest version"). Both run with
// DEBIAN_FRONTEND=noninteractive so a configuration prompt can never wedge the
// install. This is part of the OPT-IN enable flow: nothing here runs until the
// operator explicitly turns the pooler on.
func (m *Manager) InstallPackage(ctx context.Context) error {
	if m.runner == nil {
		return core.InternalError("pgbouncer: InstallPackage requires a Runner")
	}

	if _, err := m.runner.Run(ctx, exec.RunSpec{
		Name:    "apt-get",
		Args:    []string{"update"},
		Env:     []string{"DEBIAN_FRONTEND=noninteractive"},
		Timeout: commandTimeout,
	}); err != nil {
		return core.ExecError("pgbouncer: apt-get update failed").Wrap(err)
	}

	if _, err := m.runner.Run(ctx, exec.RunSpec{
		Name:    "apt-get",
		Args:    []string{"install", "-y", pkgName},
		Env:     []string{"DEBIAN_FRONTEND=noninteractive"},
		Timeout: commandTimeout,
	}); err != nil {
		return core.ExecError("pgbouncer: installing %s failed", pkgName).
			WithHint("ensure the apt sources include the pgbouncer package").Wrap(err)
	}

	m.log.InfoCtx(ctx, "installed PgBouncer package", "package", pkgName)
	return nil
}

// EnableNow enables and starts the PgBouncer service so the pooler runs now and
// survives reboots. `systemctl enable --now` is idempotent: an already-enabled,
// already-running unit is a clean no-op, which keeps the enable flow re-runnable.
func (m *Manager) EnableNow(ctx context.Context) error {
	if m.runner == nil {
		return core.InternalError("pgbouncer: EnableNow requires a Runner")
	}
	if _, err := m.runner.Run(ctx, exec.RunSpec{
		Name:    "systemctl",
		Args:    []string{"enable", "--now", serviceName},
		Timeout: commandTimeout,
	}); err != nil {
		return core.ExecError("pgbouncer: enabling the pgbouncer service failed").Wrap(err)
	}
	m.log.InfoCtx(ctx, "enabled PgBouncer service", "service", serviceName)
	return nil
}

// DisableNow stops the PgBouncer service and prevents it starting on boot — the
// inverse of EnableNow. `systemctl disable --now` is idempotent: an
// already-stopped, already-disabled unit is a clean no-op, so the disable flow is
// re-runnable.
func (m *Manager) DisableNow(ctx context.Context) error {
	if m.runner == nil {
		return core.InternalError("pgbouncer: DisableNow requires a Runner")
	}
	if _, err := m.runner.Run(ctx, exec.RunSpec{
		Name:    "systemctl",
		Args:    []string{"disable", "--now", serviceName},
		Timeout: commandTimeout,
	}); err != nil {
		return core.ExecError("pgbouncer: disabling the pgbouncer service failed").Wrap(err)
	}
	m.log.InfoCtx(ctx, "disabled PgBouncer service", "service", serviceName)
	return nil
}

// Reload applies a changed config/auth_file to a running PgBouncer with the least
// disruption. It first tries `systemctl reload` — a SIGHUP makes PgBouncer
// re-read pgbouncer.ini and reopen the auth_file WITHOUT dropping established
// client connections — and only if that fails does it fall back to a full
// `systemctl restart` (which does drop connections, but is the safe way to apply
// a change a live reload can't).
//
// Applying the config is not complete until the pooler is confirmed still up: a
// SIGHUP reload can exit 0 while PgBouncer then dies re-parsing a bad config, and
// `systemctl restart` can return before a unit that crashes on startup is caught.
// So after the systemctl command exits 0 Reload verifies the service is actually
// active (DEFAULTS.md: "reload …; verify it's still running after") and returns a
// loud, actionable error if it is not — it must never report success over a dead
// pooler. A genuinely undeterminable state ("couldn't ask systemctl") surfaces as
// the error it is, never as a silent success.
//
// The enable flow calls Reload ONLY when the rendered config or auth_file
// actually changed (EnsureConfig/EnsureUserlist report this), so an unchanged
// pooler is never needlessly bounced.
func (m *Manager) Reload(ctx context.Context) error {
	if m.runner == nil {
		return core.InternalError("pgbouncer: Reload requires a Runner")
	}

	how := "reload (SIGHUP)"
	if _, err := m.runner.Run(ctx, exec.RunSpec{
		Name:    "systemctl",
		Args:    []string{"reload", serviceName},
		Timeout: commandTimeout,
	}); err != nil {
		// A reload can legitimately be unsupported or fail mid-transition; restart is
		// the authoritative way to apply the new config. Log the cause and escalate.
		m.log.Warn("pgbouncer reload failed; falling back to restart",
			"service", serviceName, "err", err.Error())
		if _, err := m.runner.Run(ctx, exec.RunSpec{
			Name:    "systemctl",
			Args:    []string{"restart", serviceName},
			Timeout: commandTimeout,
		}); err != nil {
			return core.ExecError("pgbouncer: applying config failed — reload and restart both failed").Wrap(err)
		}
		how = "restart (reload fallback)"
	}

	// systemctl reported success, but that alone does not prove PgBouncer is live
	// (see the doc comment). Confirm the pooler is actually up before reporting the
	// config applied — a dead-after-apply pooler is a loud error, not a silent OK.
	running, err := m.IsRunning(ctx)
	if err != nil {
		return err
	}
	if !running {
		return core.ExecError("pgbouncer: applied config via %s but the pooler is not running afterward — the new config was likely rejected", how).
			WithHint("check `systemctl status pgbouncer` and the pgbouncer log, then restore the previous pgbouncer.ini / auth_file")
	}

	m.log.InfoCtx(ctx, "applied PgBouncer config", "service", serviceName, "method", how)
	return nil
}

// IsRunning reports whether the pgbouncer systemd service is active. Like the pg
// manager's IsRunning, a recognised non-active state ("inactive"/"failed"), which
// `systemctl is-active` reports with a non-zero exit, is a clean false rather than
// an error — "not running" is a normal, queryable state.
//
// It improves on that contract for the one case the enable flow relies on most —
// verifying the unit came up after starting it: an EMPTY stdout paired with a
// runner error means systemctl itself could not run (absent from PATH, a cancelled
// context, a timeout), so the state is genuinely unknown. Reporting that as the
// error it is — instead of silently "not running" — stops the orchestrator from
// mistaking "couldn't ask" for "service is down" and needlessly bouncing it.
func (m *Manager) IsRunning(ctx context.Context) (bool, error) {
	if m.runner == nil {
		return false, core.InternalError("pgbouncer: IsRunning requires a Runner")
	}
	res, err := m.runner.Run(ctx, exec.RunSpec{
		Name:    "systemctl",
		Args:    []string{"is-active", serviceName},
		Timeout: 30 * time.Second,
	})
	switch out := strings.TrimSpace(res.Stdout); {
	case out == "active":
		return true, nil
	case out != "":
		// A reported state ("inactive"/"failed"/"activating") is a definitive answer
		// even though is-active exits non-zero for it; that exit is not a failure.
		return false, nil
	case err != nil:
		// No state reported AND the command errored: we could not determine it.
		return false, core.ExecError("pgbouncer: could not determine service state").Wrap(err)
	default:
		return false, nil
	}
}
