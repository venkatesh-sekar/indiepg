package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/exec"
)

func TestUpdateScript(t *testing.T) {
	// curl is preferred when both are present.
	s, err := updateScript(true, true)
	require.NoError(t, err)
	require.Contains(t, s, "curl -fsSL")
	require.Contains(t, s, installScriptURL)
	require.Contains(t, s, "| sh")

	// wget is the fallback.
	s, err = updateScript(false, true)
	require.NoError(t, err)
	require.Contains(t, s, "wget -qO-")
	require.Contains(t, s, installScriptURL)

	// Neither present -> a clear validation error, not an opaque shell failure.
	_, err = updateScript(false, false)
	require.Error(t, err)
	require.Equal(t, core.CodeValidation, core.CodeOf(err))
}

func TestPlanUpdate(t *testing.T) {
	// Already on the resolved latest, no --force -> skip the download entirely and
	// say so. This is the core "running update again is a no-op" guarantee.
	p := planUpdate("v0.4.0", "v0.4.0", true, false)
	require.True(t, p.skip)
	require.Contains(t, p.message, "already on the latest release (v0.4.0)")
	require.Contains(t, p.message, "--force")

	// Same version but an explicit --version -> still a skip, worded for the
	// specific version rather than "latest".
	p = planUpdate("v0.4.0", "v0.4.0", false, false)
	require.True(t, p.skip)
	require.Contains(t, p.message, "already on indiepg v0.4.0")

	// --force on the same version proceeds (repair a corrupted binary).
	p = planUpdate("v0.4.0", "v0.4.0", true, true)
	require.False(t, p.skip)
	require.Contains(t, p.message, "re-installing indiepg v0.4.0")

	// A real upgrade shows current -> target and never skips.
	p = planUpdate("v0.4.0", "v0.4.1", true, false)
	require.False(t, p.skip)
	require.Contains(t, p.message, "v0.4.0 → v0.4.1")

	// A dev/local build can't be trusted to mean "this release", so it always
	// proceeds — even if the strings happen to look comparable.
	for _, dev := range []string{"dev", "", "v0.4.0-dirty", "v0.4.0-3-gabc1234"} {
		p = planUpdate(dev, "v0.4.0", true, false)
		require.Falsef(t, p.skip, "dev build %q must never skip", dev)
		require.Contains(t, p.message, "installing indiepg v0.4.0")
	}
}

func TestIsDevBuild(t *testing.T) {
	for _, v := range []string{"", "dev", "v0.4.0-dirty", "v0.4.0-3-gabc1234", "  dev  "} {
		require.Truef(t, isDevBuild(v), "%q should be a dev build", v)
	}
	for _, v := range []string{"v0.4.0", "v1.2.3", "v0.10.0"} {
		require.Falsef(t, isDevBuild(v), "%q should be a release build", v)
	}
}

func TestParseLatestTag(t *testing.T) {
	tag, err := parseLatestTag([]byte(`{"tag_name":"v0.4.1","name":"Release"}`))
	require.NoError(t, err)
	require.Equal(t, "v0.4.1", tag)

	// A response without a usable tag is an actionable error, not an empty string.
	_, err = parseLatestTag([]byte(`{"message":"Not Found"}`))
	require.Error(t, err)
	require.Equal(t, core.CodeValidation, core.CodeOf(err))

	// Malformed JSON is surfaced as an exec error rather than a panic.
	_, err = parseLatestTag([]byte(`not json`))
	require.Error(t, err)
	require.Equal(t, core.CodeExec, core.CodeOf(err))
}

func TestControlServiceErrorsWithoutSystemd(t *testing.T) {
	// Only assert the no-systemd branch when this host genuinely lacks systemctl,
	// so the test never shells out to a real `systemctl start` during `go test`.
	if systemctlAvailable() {
		t.Skip("systemctl present: cannot exercise the no-systemd branch without side effects")
	}
	err := ControlService(context.Background(), core.Discard(), ServiceStart)
	require.Error(t, err)
	require.Equal(t, core.CodeValidation, core.CodeOf(err))
}

func TestSystemdTeardownSteps(t *testing.T) {
	steps := systemdTeardownSteps()
	require.Len(t, steps, 2)
	require.Equal(t, []string{"stop", systemdServiceName}, steps[0].Args)
	require.Equal(t, []string{"disable", systemdServiceName}, steps[1].Args)
}

func TestUninstallSystemdServiceRemovesUnit(t *testing.T) {
	dir := t.TempDir()
	unit := filepath.Join(dir, "indiepg.service")
	require.NoError(t, os.WriteFile(unit, []byte("[Unit]\n"), 0o644))

	runner := exec.NewFakeRunner()
	require.NoError(t, uninstallSystemdService(context.Background(), runner, core.Discard(), unit, true))

	// Unit file is gone.
	_, statErr := os.Stat(unit)
	require.True(t, os.IsNotExist(statErr), "unit file must be removed")

	// stop, disable, and daemon-reload were all issued.
	var joined []string
	for _, c := range runner.Calls() {
		joined = append(joined, strings.Join(c.Args, " "))
	}
	require.Contains(t, joined, "stop "+systemdServiceName)
	require.Contains(t, joined, "disable "+systemdServiceName)
	require.Contains(t, joined, "daemon-reload")
}

func TestUninstallSystemdServiceToleratesMissingUnit(t *testing.T) {
	runner := exec.NewFakeRunner()
	// hasSystemctl=false -> no systemctl calls; a missing unit path is not an error.
	missing := filepath.Join(t.TempDir(), "nope.service")
	require.NoError(t, uninstallSystemdService(context.Background(), runner, core.Discard(), missing, false))
	require.Equal(t, 0, runner.CallCount(), "must not shell out when systemctl is unavailable")
}

func TestUninstallSystemdServiceContinuesOnSystemctlError(t *testing.T) {
	dir := t.TempDir()
	unit := filepath.Join(dir, "indiepg.service")
	require.NoError(t, os.WriteFile(unit, []byte("x"), 0o644))

	// A failing `stop` (e.g. service already gone) must not block teardown.
	runner := exec.NewFakeRunner().On("stop", exec.FakeResponse{ExitCode: 1, Err: errors.New("unit not loaded")})
	require.NoError(t, uninstallSystemdService(context.Background(), runner, core.Discard(), unit, true))

	_, statErr := os.Stat(unit)
	require.True(t, os.IsNotExist(statErr), "unit must still be removed despite the stop failure")
}

func TestRemoveStateFiles(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "indiepg")
	require.NoError(t, os.MkdirAll(stateDir, 0o755))
	state := filepath.Join(stateDir, "indiepg.db")
	for _, p := range []string{state, state + "-wal", state + "-shm"} {
		require.NoError(t, os.WriteFile(p, []byte("x"), 0o600))
	}

	removed := removeStateFiles(state, core.Discard())
	require.Contains(t, removed, state)
	require.Contains(t, removed, state+"-wal")
	require.Contains(t, removed, state+"-shm")

	// The now-empty state dir is dropped too.
	require.Contains(t, removed, stateDir)
	_, err := os.Stat(stateDir)
	require.True(t, os.IsNotExist(err))
}

func TestRemoveStateFilesIsIdempotent(t *testing.T) {
	// Removing a state path with nothing on disk returns no error and removes
	// nothing — a re-run of `uninstall --purge` must be safe.
	state := filepath.Join(t.TempDir(), "missing", "indiepg.db")
	removed := removeStateFiles(state, core.Discard())
	require.Empty(t, removed)
}
