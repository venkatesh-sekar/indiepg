package pgbouncer

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

// The runtime dir must be derived from the SAME pidfile the rendered config
// points at, so the directory we provision always matches pgbouncer.ini. The
// default config renders defaultPidFile, whose parent is what we provision.
func TestBouncerRuntimeDir_DerivedFromConfiguredPidfile(t *testing.T) {
	require.Equal(t, defaultPidFile, bouncerPidFile())
	require.Equal(t, filepath.Dir(defaultPidFile), bouncerRuntimeDir())

	// And the rendered config actually uses that pidfile, so the two stay in sync.
	cfg, err := RenderConfig(ConfigParams{Pool: RecommendPool(100, "")})
	require.NoError(t, err)
	require.Contains(t, cfg, "pidfile = "+bouncerPidFile())
}

// The drop-in must set RuntimeDirectory to the basename of the runtime dir
// (relative to /run) so systemd recreates /run/<name> on every start, plus a
// 0755 mode and the managed marker.
func TestRenderRuntimeDropin_SetsRuntimeDirectoryFromBasename(t *testing.T) {
	got := renderRuntimeDropin()
	require.True(t, strings.HasPrefix(got, runtimeDropinMarker+"\n"), "drop-in must start with the managed marker")
	require.Contains(t, got, "[Service]\n")
	require.Contains(t, got, "RuntimeDirectory="+filepath.Base(bouncerRuntimeDir())+"\n")
	require.Contains(t, got, "RuntimeDirectoryMode=0755\n")
}

// EnsureRuntimeDir installs the drop-in under <systemdDir>/pgbouncer.service.d/
// and reloads systemd so the RuntimeDirectory= directive is live before the unit
// starts.
func TestEnsureRuntimeDir_WritesDropinThenDaemonReloads(t *testing.T) {
	r := exec.NewFakeRunner()
	m := New(Options{Runner: r, Logger: core.Discard(), ConfDir: t.TempDir(), SystemdDir: t.TempDir()})

	require.NoError(t, m.EnsureRuntimeDir(context.Background()))

	// The drop-in landed at the canonical pgbouncer.service.d path with our content.
	path := filepath.Join(m.systemdDir, serviceName+".service.d", runtimeDropinName)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, renderRuntimeDropin(), string(data))

	// daemon-reload ran, and before any `install -d` provisioning so systemd has the
	// directive when the unit comes up.
	var order []string
	for _, c := range r.Calls() {
		order = append(order, argvOf(c))
	}
	require.Contains(t, strings.Join(order, "\n"), "systemctl daemon-reload")
}

func TestEnsureRuntimeDir_RequiresRunner(t *testing.T) {
	m := New(Options{Logger: core.Discard(), SystemdDir: t.TempDir()})
	err := m.EnsureRuntimeDir(context.Background())
	require.Error(t, err)
	require.Equal(t, core.CodeInternal, core.CodeOf(err))
}

func TestEnsureRuntimeDir_DaemonReloadFailurePropagates(t *testing.T) {
	r := exec.NewFakeRunner()
	r.On("daemon-reload", exec.FakeResponse{Err: errors.New("systemd busy")})
	m := New(Options{Runner: r, Logger: core.Discard(), SystemdDir: t.TempDir()})

	err := m.EnsureRuntimeDir(context.Background())
	require.Error(t, err)
	require.Equal(t, core.CodeExec, core.CodeOf(err))
}
