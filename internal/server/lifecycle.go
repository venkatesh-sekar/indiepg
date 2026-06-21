package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/exec"
)

// installScriptURL is the canonical one-line installer. `indiepg update` re-runs
// it (with INDIEPG_NO_INSTALL set) to fetch and checksum-verify the latest
// release binary over the current one; see scripts/install.sh.
const installScriptURL = "https://raw.githubusercontent.com/venkatesh-sekar/indiepg/main/scripts/install.sh"

// githubLatestReleaseAPI returns the latest published release as JSON. `update`
// resolves the tag here (in Go) — not only inside the installer — so it can
// compare the result against the running build and skip a redundant download.
const githubLatestReleaseAPI = "https://api.github.com/repos/venkatesh-sekar/indiepg/releases/latest"

// UpdateOptions drive `indiepg update`.
type UpdateOptions struct {
	Logger *core.Logger
	// Version is the release tag to install; empty means latest.
	Version string
	// Force reinstalls the target version even when it is already running.
	Force bool
}

// Update upgrades indiepg in place: it re-runs the release installer to download
// and checksum-verify the requested (default: latest) binary over the existing
// one, then restarts the systemd service so the new binary takes over.
//
// Update is strictly a binary swap. It does NOT touch the admin password, the
// panel config, or the PostgreSQL cluster — the installer's own `indiepg install`
// hand-off is suppressed via INDIEPG_NO_INSTALL precisely so update never
// re-provisions Postgres or rotates the admin credential; the service restart is
// what activates the new binary.
func Update(ctx context.Context, opts UpdateOptions) error {
	log := opts.Logger
	if log == nil {
		log = core.Discard()
	}

	// Decide exactly which version we're heading to BEFORE touching anything, so we
	// can show the operator current → target and skip the whole download + restart
	// when they're already on it. An explicit --version is taken as-is; otherwise
	// we resolve "latest" from the GitHub API here. (The installer would resolve
	// "latest" too, but doing it in Go is what lets us compare and bail.) This is a
	// read-only check, so it deliberately runs before the root requirement below:
	// anyone can ask "am I up to date?" without sudo.
	current := strings.TrimSpace(core.Version)
	wantLatest := strings.TrimSpace(opts.Version) == ""
	target := strings.TrimSpace(opts.Version)
	if wantLatest {
		log.Info("resolving latest indiepg release")
		var err error
		target, err = resolveLatestVersion(ctx)
		if err != nil {
			return err
		}
	}

	plan := planUpdate(current, target, wantLatest, opts.Force)
	if plan.skip {
		fmt.Fprintln(os.Stdout, "indiepg: "+plan.message)
		log.Info("already on the requested version; nothing to do", "version", current, "target", target)
		return nil
	}

	// There's something to install — that mutates /usr/local/bin and restarts the
	// service, both of which need root and a downloader. Check now (not earlier),
	// with a one-line fix, rather than letting the piped installer die with a
	// "must run as root" buried deep in its output.
	if os.Geteuid() != 0 {
		return core.ValidationError("update must run as root — try: sudo indiepg update")
	}
	script, err := updateScript(haveExecutable("curl"), haveExecutable("wget"))
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, "indiepg: "+plan.message)

	// Download + install the new binary with stdio wired to this terminal so the
	// operator sees live progress and any checksum/verification output. This is a
	// direct privileged orchestration step (it replaces the installed binary), so
	// it bypasses the captured exec.Runner by design. We pin INDIEPG_VERSION to the
	// version we just resolved so the installer doesn't re-resolve "latest" (one
	// fewer API call, and no race if "latest" moves mid-update).
	log.Info("downloading indiepg release binary", "version", target)
	dl := osexec.CommandContext(ctx, "sh", "-c", script)
	dl.Stdin, dl.Stdout, dl.Stderr = os.Stdin, os.Stdout, os.Stderr
	dl.Env = append(os.Environ(), "INDIEPG_NO_INSTALL=1", "INDIEPG_VERSION="+target)
	if err := dl.Run(); err != nil {
		return core.ExecError("update: download/install of the new binary failed").Wrap(err)
	}

	// Read back what actually landed on disk — the running process is still the
	// OLD binary, so exec'ing the new one is the only way to confirm the swap.
	installed := installedBinaryVersion(ctx, target)

	// Restart the service so it execs the freshly-installed binary. On a
	// non-systemd host there is nothing to restart — the binary is in place and
	// the operator restarts however they run `indiepg serve`.
	if !systemctlAvailable() {
		log.Warn("systemctl not found; binary updated — restart `indiepg serve` to apply")
		announceUpdateSummary(false, current, installed)
		return nil
	}
	runner := exec.NewOSRunner(log, false)
	if _, err := runner.Run(ctx, exec.RunSpec{
		Name: "systemctl", Args: []string{"restart", systemdServiceName}, Timeout: 60 * time.Second,
	}); err != nil {
		return err
	}
	log.Info("indiepg updated and service restarted", "service", systemdServiceName, "from", current, "to", installed)
	announceUpdateSummary(true, current, installed)
	return nil
}

// updatePlan is the decision planUpdate makes from the current and target
// versions: whether to skip the download entirely, plus the line shown to the
// operator either way.
type updatePlan struct {
	skip    bool
	message string
}

// planUpdate decides whether `update` has anything to do. It skips only when the
// running binary is a clean release build that already matches the target and
// --force was not passed. A dev or locally-built binary (the "dev" default, a
// "-dirty" tree, or a "git describe" build past a tag) always proceeds, since its
// version string can't be trusted to mean "this exact published release".
func planUpdate(current, target string, wantLatest, force bool) updatePlan {
	dev := isDevBuild(current)
	switch {
	case !force && !dev && current == target:
		which := fmt.Sprintf("indiepg %s", current)
		if wantLatest {
			which = fmt.Sprintf("the latest release (%s)", current)
		}
		return updatePlan{skip: true, message: fmt.Sprintf(
			"already on %s; nothing to do. Re-install it anyway with: indiepg update --force", which)}
	case force && !dev && current == target:
		return updatePlan{message: fmt.Sprintf("re-installing indiepg %s (--force)", target)}
	case dev:
		return updatePlan{message: fmt.Sprintf("installing indiepg %s over the current build (%s)", target, current)}
	default:
		return updatePlan{message: fmt.Sprintf("updating indiepg: %s → %s", current, target)}
	}
}

// isDevBuild reports whether a version string is a local/development build rather
// than a clean published release tag. Released binaries are stamped with the bare
// git tag (e.g. "v0.4.0"); the Makefile's `git describe` default yields "dev", a
// "-dirty" suffix, or a "-<n>-g<sha>" suffix for anything else.
func isDevBuild(v string) bool {
	v = strings.TrimSpace(v)
	return v == "" || v == "dev" || strings.HasSuffix(v, "-dirty") || strings.Contains(v, "-g")
}

// resolveLatestVersion asks the GitHub API for the latest release tag. It mirrors
// the resolution the installer does, but in Go so `update` can compare it against
// the running version and short-circuit. Failures are actionable: a rate-limit or
// outage points the operator at --version rather than dead-ending.
func resolveLatestVersion(ctx context.Context) (string, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, githubLatestReleaseAPI, nil)
	if err != nil {
		return "", core.InternalError("update: build GitHub release request").Wrap(err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", core.ExecError(
			"update: could not reach the GitHub API to find the latest release — network down? " +
				"Pass --version vX.Y.Z to skip resolution").Wrap(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	switch {
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests:
		return "", core.ExecError(
			"update: GitHub rate-limited this host (HTTP %d) while finding the latest release — "+
				"wait a few minutes, or pass --version vX.Y.Z to skip resolution", resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return "", core.ExecError(
			"update: GitHub returned HTTP %d while finding the latest release — "+
				"pass --version vX.Y.Z to skip resolution", resp.StatusCode)
	}
	return parseLatestTag(body)
}

// parseLatestTag pulls tag_name from a GitHub "latest release" JSON body. Kept
// pure so the parsing is unit-tested without a network round-trip.
func parseLatestTag(body []byte) (string, error) {
	var rel struct {
		TagName string `json:"tag_name"`
	}
	if err := json.Unmarshal(body, &rel); err != nil {
		return "", core.ExecError("update: could not parse the GitHub release response").Wrap(err)
	}
	tag := strings.TrimSpace(rel.TagName)
	if tag == "" {
		return "", core.ValidationError(
			"update: no published release found — pass --version vX.Y.Z, or build locally (see README)")
	}
	return tag, nil
}

// installedBinaryVersion best-effort reads the version of the binary now on disk
// by exec'ing `indiepg version` on it. The running process is still the OLD
// binary, so this is the only honest way to confirm what the swap produced. On
// any failure it falls back to the version we asked the installer for.
func installedBinaryVersion(ctx context.Context, fallback string) string {
	bin := resolveExecPath()
	if bin == "" {
		return fallback
	}
	out, err := osexec.CommandContext(ctx, bin, "version").Output()
	if err != nil {
		return fallback
	}
	if v := strings.TrimSpace(string(out)); v != "" {
		return v
	}
	return fallback
}

// updateScript returns the shell pipeline that fetches and runs the installer,
// preferring curl and falling back to wget. It errors when neither is present so
// the failure is a clear, actionable message rather than an opaque `sh` error.
func updateScript(haveCurl, haveWget bool) (string, error) {
	switch {
	case haveCurl:
		return "curl -fsSL " + installScriptURL + " | sh", nil
	case haveWget:
		return "wget -qO- " + installScriptURL + " | sh", nil
	default:
		return "", core.ValidationError("update: need curl or wget on PATH to download the release")
	}
}

// haveExecutable reports whether name is found on PATH.
func haveExecutable(name string) bool {
	_, err := osexec.LookPath(name)
	return err == nil
}

// ServiceAction is a systemctl verb indiepg exposes as a convenience subcommand
// so operators don't have to remember the unit name.
type ServiceAction string

const (
	ServiceStart   ServiceAction = "start"
	ServiceStop    ServiceAction = "stop"
	ServiceRestart ServiceAction = "restart"
)

// ControlService runs `systemctl <action> indiepg` for start/stop/restart. It is
// a thin convenience over systemd: the service is the source of truth, so this
// only shells out and reports the result. On a non-systemd host there is nothing
// to control, so it returns a clear, actionable error rather than a silent no-op.
func ControlService(ctx context.Context, log *core.Logger, action ServiceAction) error {
	if log == nil {
		log = core.Discard()
	}
	if !systemctlAvailable() {
		return core.ValidationError(
			"%s: systemctl not found — this host has no systemd service to %s; run `indiepg serve` directly",
			action, action)
	}

	runner := exec.NewOSRunner(log, false)
	if _, err := runner.Run(ctx, exec.RunSpec{
		Name: "systemctl", Args: []string{string(action), systemdServiceName}, Timeout: 60 * time.Second,
	}); err != nil {
		return err
	}
	log.Info("service "+string(action), "service", systemdServiceName)
	fmt.Fprintf(os.Stdout, "indiepg: ran systemctl %s %s\n", action, systemdServiceName)
	return nil
}

// UninstallOptions drive `indiepg uninstall`.
type UninstallOptions struct {
	Logger *core.Logger
	// StatePath is the panel state DB; only removed when Purge is set. Empty
	// falls back to the canonical location.
	StatePath string
	// Purge additionally deletes the panel state DB and the indiepg binary.
	Purge bool
}

// Uninstall removes indiepg's own footprint: it stops and disables the systemd
// service and deletes the unit file. With Purge it also deletes the panel state
// database (admin password, config, instance identity) and the indiepg binary.
//
// It NEVER touches the PostgreSQL cluster or any database it manages — that data
// is the whole point of the box and removing it is left to the operator. The
// summary spells this out and prints the manual steps for anyone who truly wants
// a full Postgres teardown.
func Uninstall(ctx context.Context, opts UninstallOptions) error {
	log := opts.Logger
	if log == nil {
		log = core.Discard()
	}

	runner := exec.NewOSRunner(log, false)
	if err := uninstallSystemdService(ctx, runner, log, systemdUnitPath, systemctlAvailable()); err != nil {
		return err
	}

	var removed []string
	if opts.Purge {
		statePath := opts.StatePath
		if statePath == "" {
			statePath = fallbackStatePath
		}
		removed = append(removed, removeStateFiles(statePath, log)...)
		if bin := resolveExecPath(); bin != "" {
			switch err := os.Remove(bin); {
			case err == nil:
				removed = append(removed, bin)
			case !os.IsNotExist(err):
				log.Warn("uninstall: could not remove binary", "path", bin, "err", err)
			}
		}
	}

	announceUninstallSummary(opts.Purge, removed)
	log.Info("uninstall complete", "purged", opts.Purge)
	return nil
}

// systemdTeardownSteps are the ordered systemctl calls that take the service
// down. They are best-effort during uninstall (a partially-installed or
// already-stopped service must not block teardown), so callers run them
// tolerantly. Kept pure for testing.
func systemdTeardownSteps() []exec.RunSpec {
	return []exec.RunSpec{
		{Name: "systemctl", Args: []string{"stop", systemdServiceName}, Timeout: 60 * time.Second},
		{Name: "systemctl", Args: []string{"disable", systemdServiceName}, Timeout: 30 * time.Second},
	}
}

// uninstallSystemdService stops+disables the service (best-effort), removes the
// unit file, and reloads systemd. unitPath and hasSystemctl are parameters so
// the whole teardown is unit-testable without a real systemd or writing to /etc.
// A failing systemctl step is logged and skipped — teardown must still complete
// on a half-installed box; only an unexpected unit-file removal error aborts.
func uninstallSystemdService(ctx context.Context, runner exec.Runner, log *core.Logger, unitPath string, hasSystemctl bool) error {
	if hasSystemctl {
		for _, step := range systemdTeardownSteps() {
			if _, err := runner.Run(ctx, step); err != nil {
				log.Warn("uninstall: systemctl step failed (continuing)", "args", step.Args, "err", err)
			}
		}
	}

	if err := os.Remove(unitPath); err != nil && !os.IsNotExist(err) {
		return core.InternalError("uninstall: remove systemd unit %q", unitPath).Wrap(err)
	}
	log.Info("removed systemd unit", "path", unitPath)

	if hasSystemctl {
		if _, err := runner.Run(ctx, exec.RunSpec{
			Name: "systemctl", Args: []string{"daemon-reload"}, Timeout: 30 * time.Second,
		}); err != nil {
			log.Warn("uninstall: daemon-reload failed (continuing)", "err", err)
		}
	}
	return nil
}

// removeStateFiles deletes the SQLite state DB and its WAL/SHM/journal sidecars,
// then removes the containing directory if it is now empty. It returns the paths
// it actually removed. Missing files are not errors (idempotent purge).
func removeStateFiles(statePath string, log *core.Logger) []string {
	var removed []string
	for _, p := range []string{statePath, statePath + "-wal", statePath + "-shm", statePath + "-journal"} {
		switch err := os.Remove(p); {
		case err == nil:
			removed = append(removed, p)
		case !os.IsNotExist(err):
			log.Warn("uninstall: could not remove state file", "path", p, "err", err)
		}
	}
	// Best-effort: drop the now-empty state dir (e.g. /var/lib/indiepg). os.Remove
	// only succeeds on an empty directory, so a dir holding other files is left be.
	dir := filepath.Dir(statePath)
	if dir != "" && dir != "/" && dir != "." {
		if err := os.Remove(dir); err == nil {
			removed = append(removed, dir)
		}
	}
	return removed
}

// announceUpdateSummary prints the end-of-update block to stdout, reassuring the
// operator that only the binary changed and stating the exact version now on
// disk. from is the version replaced (may be empty/unknown); to is what the swap
// produced, read back from the new binary.
func announceUpdateSummary(serviceRestarted bool, from, to string) {
	const banner = "============================================================"
	out := os.Stdout
	out.WriteString("\n" + banner + "\n")
	if from != "" && from != to {
		fmt.Fprintf(out, "  indiepg updated: %s → %s\n", from, to)
	} else {
		fmt.Fprintf(out, "  indiepg reinstalled at %s.\n", to)
	}
	if serviceRestarted {
		out.WriteString("  The systemd service was restarted on the new version.\n")
	} else {
		out.WriteString("  Restart it to apply:   indiepg serve\n")
	}
	out.WriteString("  Your admin password, config, and databases are unchanged.\n")
	fmt.Fprintf(out, "  Now running:           %s\n", to)
	out.WriteString(banner + "\n\n")
}

// announceUninstallSummary prints the end-of-uninstall block to stdout. It is
// explicit that PostgreSQL and its data are untouched, since that is the one
// thing an operator must not assume an "uninstall" removed.
func announceUninstallSummary(purged bool, removed []string) {
	const banner = "============================================================"
	out := os.Stdout
	out.WriteString("\n" + banner + "\n")
	out.WriteString("  indiepg has been uninstalled.\n")
	out.WriteString("  The systemd service was stopped, disabled, and removed.\n")
	if purged {
		out.WriteString("\n  Purged:\n")
		if len(removed) == 0 {
			out.WriteString("    (nothing left to remove)\n")
		}
		for _, p := range removed {
			fmt.Fprintf(out, "    - %s\n", p)
		}
	} else {
		out.WriteString("  The panel state DB and binary were left in place — re-run\n")
		out.WriteString("  `indiepg install`, or `indiepg uninstall --purge` to remove them.\n")
	}
	out.WriteString("\n")
	out.WriteString("  PostgreSQL and all your databases are UNTOUCHED.\n")
	out.WriteString("  To remove Postgres too (THIS DESTROYS DATA) do it by hand, e.g.:\n")
	out.WriteString("    sudo apt-get purge 'postgresql*'\n")
	out.WriteString(banner + "\n\n")
}
