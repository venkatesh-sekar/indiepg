package pgbouncer

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/exec"
)

// runtimeDropinName is the systemd drop-in indiepg installs under
// <systemdDir>/pgbouncer.service.d/. The numeric prefix orders it after a
// distro-shipped drop-in and the indiepg suffix marks it as ours, mirroring the
// `10-indiepg-*` convention used for other locally-administered units.
const runtimeDropinName = "10-indiepg-runtime.conf"

// runtimeDropinMarker is the first line of the managed drop-in, identifying it as
// indiepg's so an operator can tell where the RuntimeDirectory= came from.
const runtimeDropinMarker = "# managed by indiepg — provisions PgBouncer's runtime (pidfile) directory; do not edit by hand"

// bouncerPidFile is the pidfile path the enable flow bakes into pgbouncer.ini.
// Enable leaves ConfigParams.PidFile unset, so EnsureConfig renders defaultPidFile;
// deriving the runtime directory from the SAME constant keeps the directory we
// provision and the pidfile pgbouncer.ini actually points at in lockstep.
func bouncerPidFile() string { return defaultPidFile }

// bouncerRuntimeDir is the directory PgBouncer must be able to write its pidfile
// into (e.g. /var/run/pgbouncer for the default /var/run/pgbouncer/pgbouncer.pid).
// On a clean Debian box the package provisions this tmpfs directory at BOOT (via
// systemd-tmpfiles / the unit's RuntimeDirectory=); but indiepg apt-installs
// pgbouncer at enable time — AFTER systemd has booted — so that provisioning never
// runs and the directory is missing, which makes `systemctl enable --now pgbouncer`
// fail with "could not open pidfile ... No such file or directory".
func bouncerRuntimeDir() string { return filepath.Dir(bouncerPidFile()) }

// EnsureRuntimeDir guarantees PgBouncer's pidfile directory (bouncerRuntimeDir)
// exists with the correct ownership BEFORE the service is started, and keeps it
// that way across reboots. It is the fix for the post-boot apt-install race: the
// package's boot-time runtime provisioning never ran, so the daemon could not
// write its pidfile and the unit failed to start.
//
// It applies two complementary measures:
//  1. PRIMARY (declarative, reboot-safe): a pgbouncer.service drop-in that sets
//     RuntimeDirectory=, so systemd (re)creates /run/<name> owned by the unit's
//     User= on EVERY start — covering both the already-booted case here and every
//     future reboot.
//  2. BELT-AND-SUSPENDERS (immediate): an explicit `install -d` of the directory
//     now, owned by the resolved pooler user, so provisioning still happens even on
//     a distro whose unit provisions its runtime dir by some other path.
func (m *Manager) EnsureRuntimeDir(ctx context.Context) error {
	if m.runner == nil {
		return core.InternalError("pgbouncer: EnsureRuntimeDir requires a Runner")
	}
	if err := m.installRuntimeDropin(ctx); err != nil {
		return err
	}
	return m.ensurePidfileDir(ctx)
}

// installRuntimeDropin writes a systemd drop-in adding RuntimeDirectory= to
// pgbouncer.service, then runs `systemctl daemon-reload` so systemd picks up the
// change before the unit is (re)started. With RuntimeDirectory=<name>, systemd
// creates /run/<name> owned by the unit's User= on every start, which fixes both
// the immediate already-booted case and reboot persistence.
//
// The drop-in is written directly (it holds no secrets, only a directory name),
// mirroring how the panel writes its own systemd unit; the daemon-reload goes
// through the injected runner so it stays observable/fakeable. The write target is
// m.systemdDir (overridable in tests) so this never touches the real /etc when unit
// tested.
func (m *Manager) installRuntimeDropin(ctx context.Context) error {
	dropinDir := filepath.Join(m.systemdDir, serviceName+".service.d")
	if err := os.MkdirAll(dropinDir, 0o755); err != nil {
		return core.InternalError("pgbouncer: create systemd drop-in dir %q", dropinDir).Wrap(err)
	}

	path := filepath.Join(dropinDir, runtimeDropinName)
	if err := os.WriteFile(path, []byte(renderRuntimeDropin()), 0o644); err != nil {
		return core.InternalError("pgbouncer: write systemd runtime drop-in %q", path).Wrap(err)
	}
	m.log.InfoCtx(ctx, "wrote PgBouncer runtime drop-in", "path", path)

	if _, err := m.runner.Run(ctx, exec.RunSpec{
		Name:    "systemctl",
		Args:    []string{"daemon-reload"},
		Timeout: commandTimeout,
	}); err != nil {
		return core.ExecError("pgbouncer: systemctl daemon-reload after writing the runtime drop-in failed").Wrap(err)
	}
	return nil
}

// renderRuntimeDropin builds the drop-in content. RuntimeDirectory takes a name
// RELATIVE to /run, so it is the basename of bouncerRuntimeDir (e.g. "pgbouncer"
// for /var/run/pgbouncer, where /var/run is the conventional symlink to /run).
// RuntimeDirectoryMode pins the directory to 0755 so the (non-root) daemon can
// create its pidfile while the path stays world-readable but not world-writable.
func renderRuntimeDropin() string {
	name := filepath.Base(bouncerRuntimeDir())
	return runtimeDropinMarker + "\n" +
		"[Service]\n" +
		"RuntimeDirectory=" + name + "\n" +
		"RuntimeDirectoryMode=0755\n"
}

// ensurePidfileDir explicitly provisions the pidfile directory right now, as a
// belt-and-suspenders backstop to the drop-in: even if a distro provisions the
// unit's runtime directory by a different mechanism, the directory PgBouncer writes
// its pidfile into exists before the start. The directory is DERIVED from the
// configured pidfile (bouncerRuntimeDir) so it always matches pgbouncer.ini, and is
// owned by the SAME user the pooler runs as (resolveBouncerOwner — the dedicated
// `pgbouncer` user when present, otherwise `postgres`) so the non-root daemon can
// create its pidfile. `install -d` (mkdir + chmod + chown in one) goes through the
// runner so the step stays observable/fakeable.
//
// Owner resolution mirrors chownToBouncerUser's root contract: under root a
// resolve failure is fatal (a wrong-owner runtime dir would silently break the
// pooler); when not running as root (tests, hands-on dev) we cannot chown to
// another user anyway and the drop-in still covers the real boot path, so the
// explicit create is skipped rather than failing.
func (m *Manager) ensurePidfileDir(ctx context.Context) error {
	dir := bouncerRuntimeDir()
	_, _, owner, err := resolveBouncerOwner()
	if err != nil {
		if os.Geteuid() == 0 {
			return core.InternalError(
				"pgbouncer: resolve an OS user (tried %s) to own the runtime dir %q",
				strings.Join(bouncerOwnerCandidates, ", "), dir,
			).Wrap(err)
		}
		m.log.InfoCtx(ctx, "skipping explicit PgBouncer runtime dir provisioning (non-root, owner unresolved)", "dir", dir)
		return nil
	}

	if _, err := m.runner.Run(ctx, exec.RunSpec{
		Name:    "install",
		Args:    []string{"-d", "-m", "0755", "-o", owner, "-g", owner, dir},
		Timeout: commandTimeout,
	}); err != nil {
		return core.ExecError("pgbouncer: creating the runtime dir %q failed", dir).Wrap(err)
	}
	m.log.InfoCtx(ctx, "ensured PgBouncer runtime dir", "dir", dir, "owner", owner)
	return nil
}
