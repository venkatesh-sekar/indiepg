package server

import (
	"context"
	"os"
	osexec "os/exec"
	"path/filepath"
	"time"

	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/exec"
)

// systemdServiceName is the unit name indiepg installs and manages.
const systemdServiceName = "indiepg"

// systemdUnitPath is where the generated unit is written. It is the standard
// location for locally-administered units.
const systemdUnitPath = "/etc/systemd/system/indiepg.service"

// renderSystemdUnit builds the unit file content for the panel. It is pure so it
// can be unit-tested without touching the filesystem. execPath is the absolute
// path to the indiepg binary and statePath the SQLite state file the service
// should serve from — both are baked into ExecStart so the service is
// self-contained and reboot-safe.
//
// The service runs as root (no User= line): indiepg manages the native Postgres
// via systemctl/apt and connects over the local unix socket, so it needs root
// on the box. The panel binds a private address by config invariant, so this is
// a trusted-single-box posture, not a public service.
func renderSystemdUnit(execPath, statePath string) string {
	return `[Unit]
Description=indiepg — private Postgres admin panel
Documentation=https://github.com/venkatesh-sekar/indiepg
After=network-online.target postgresql.service
Wants=network-online.target

[Service]
Type=simple
ExecStart="` + execPath + `" serve --state "` + statePath + `"
Restart=on-failure
RestartSec=5s

[Install]
WantedBy=multi-user.target
`
}

// systemctlAvailable reports whether this is a systemd box (systemctl on PATH).
// On a non-systemd host install falls back to printing manual start steps
// instead of failing.
func systemctlAvailable() bool {
	_, err := osexec.LookPath("systemctl")
	return err == nil
}

// resolveExecPath returns the absolute, symlink-resolved path of the running
// binary for the unit's ExecStart, falling back to os.Args[0] if the runtime
// cannot report it. Resolving symlinks means a binary reached via a /usr/local/bin
// symlink still produces a stable ExecStart.
func resolveExecPath() string {
	p, err := os.Executable()
	if err != nil || p == "" {
		return os.Args[0]
	}
	if resolved, rerr := filepath.EvalSymlinks(p); rerr == nil && resolved != "" {
		return resolved
	}
	return p
}

// installSystemdService writes the unit, reloads systemd, enables it for boot,
// and (re)starts it so a single `indiepg install` leaves a running, reboot-safe
// service with no second command. A plain `restart` (rather than `enable --now`)
// is used so an idempotent re-install with a changed unit — e.g. a different
// --state path or a moved binary — actually takes effect on the live process
// instead of leaving it on the stale ExecStart. The systemctl calls go through
// the injected runner so they are observable/fakeable; the file write is a
// direct privileged op (the unit holds no secrets, only the exec/state paths).
func installSystemdService(ctx context.Context, runner exec.Runner, log *core.Logger, execPath, statePath string) error {
	unit := renderSystemdUnit(execPath, statePath)
	if err := os.WriteFile(systemdUnitPath, []byte(unit), 0o644); err != nil {
		return core.InternalError("install: write systemd unit %q", systemdUnitPath).Wrap(err)
	}
	log.Info("wrote systemd unit", "path", systemdUnitPath)

	steps := []exec.RunSpec{
		{Name: "systemctl", Args: []string{"daemon-reload"}, Timeout: 30 * time.Second},
		{Name: "systemctl", Args: []string{"enable", systemdServiceName}, Timeout: 30 * time.Second},
		{Name: "systemctl", Args: []string{"restart", systemdServiceName}, Timeout: 60 * time.Second},
	}
	for _, step := range steps {
		if _, err := runner.Run(ctx, step); err != nil {
			return err
		}
	}
	log.Info("systemd service enabled and started", "service", systemdServiceName)
	return nil
}
