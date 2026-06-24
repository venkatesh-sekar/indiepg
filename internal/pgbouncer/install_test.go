package pgbouncer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/exec"
	"github.com/venkatesh-sekar/indiepg/internal/pg"
)

// newInstallManager builds a Manager whose config dir is a throwaway temp dir so
// EnsureConfig is exercisable off-root.
func newInstallManager(t *testing.T) *Manager {
	t.Helper()
	return New(Options{
		Runner:  exec.NewFakeRunner(),
		Logger:  core.Discard(),
		ConfDir: t.TempDir(),
	})
}

// baseParams is a valid, host-sized ConfigParams.
func baseParams() ConfigParams {
	return ConfigParams{Pool: RecommendPool(100, pg.ProfileMixed)}
}

func TestEnsureConfig_WritesMarkedFile0640(t *testing.T) {
	m := newInstallManager(t)

	changed, err := m.EnsureConfig(context.Background(), baseParams())
	require.NoError(t, err)
	require.True(t, changed)

	data, err := os.ReadFile(m.ConfigPath())
	require.NoError(t, err)
	require.True(t, HasManagedMarker(string(data)), "written config must carry the managed marker")
	require.Contains(t, string(data), "pool_mode = transaction")

	info, err := os.Stat(m.ConfigPath())
	require.NoError(t, err)
	require.Equal(t, confFileMode, info.Mode().Perm(), "config must be 0640")
}

func TestEnsureConfig_IdempotentNoRewriteWhenUnchanged(t *testing.T) {
	m := newInstallManager(t)

	changed, err := m.EnsureConfig(context.Background(), baseParams())
	require.NoError(t, err)
	require.True(t, changed)

	// Record the inode so we can prove the second call did not rewrite the file.
	before, err := os.Stat(m.ConfigPath())
	require.NoError(t, err)

	changed, err = m.EnsureConfig(context.Background(), baseParams())
	require.NoError(t, err)
	require.False(t, changed, "an unchanged (deterministic) config must be a no-op")

	after, err := os.Stat(m.ConfigPath())
	require.NoError(t, err)
	require.Equal(t, before.ModTime(), after.ModTime(), "no-op must not rewrite the file")
}

func TestEnsureConfig_RewritesWhenChanged(t *testing.T) {
	m := newInstallManager(t)

	_, err := m.EnsureConfig(context.Background(), baseParams())
	require.NoError(t, err)

	// A different pool sizing yields different content → a rewrite.
	p := ConfigParams{Pool: RecommendPool(200, pg.ProfileOLTP)}
	changed, err := m.EnsureConfig(context.Background(), p)
	require.NoError(t, err)
	require.True(t, changed)

	data, _ := os.ReadFile(m.ConfigPath())
	require.Contains(t, string(data), "default_pool_size = "+
		// 200 max_conn, OLTP util 0.80 → (200-5)*0.80 = 156
		"156")
}

// TestEnsureConfig_RefusesForeignConfig is the safety-critical case: a config
// file indiepg did NOT write (no marker) must never be clobbered.
func TestEnsureConfig_RefusesForeignConfig(t *testing.T) {
	m := newInstallManager(t)

	require.NoError(t, os.MkdirAll(filepath.Dir(m.ConfigPath()), 0o755))
	foreign := "[databases]\nmydb = host=10.0.0.5 port=5432\n# my own pooler\n"
	require.NoError(t, os.WriteFile(m.ConfigPath(), []byte(foreign), 0o640))

	changed, err := m.EnsureConfig(context.Background(), baseParams())
	require.Error(t, err)
	require.False(t, changed)
	require.Equal(t, core.CodeConflict, core.CodeOf(err))

	// The operator's file is untouched.
	data, _ := os.ReadFile(m.ConfigPath())
	require.Equal(t, foreign, string(data))
}

// TestEnsureConfig_RefusesMarkerNotAtFirstLine guards against a foreign file that
// merely quotes the marker text in a mid-file comment — only a marker on the
// FIRST line proves indiepg ownership.
func TestEnsureConfig_RefusesMarkerNotAtFirstLine(t *testing.T) {
	m := newInstallManager(t)

	require.NoError(t, os.MkdirAll(filepath.Dir(m.ConfigPath()), 0o755))
	foreign := "[databases]\n# " + ConfigMarker + "\nmydb = host=10.0.0.5\n"
	require.NoError(t, os.WriteFile(m.ConfigPath(), []byte(foreign), 0o640))

	_, err := m.EnsureConfig(context.Background(), baseParams())
	require.Error(t, err)
	require.Equal(t, core.CodeConflict, core.CodeOf(err))
	data, _ := os.ReadFile(m.ConfigPath())
	require.Equal(t, foreign, string(data))
}

// TestEnsureConfig_RefusesSymlinkAtConfigPath proves O_NOFOLLOW refuses to follow
// a symlink planted at the config path (a path-hijack guard) and reports it as a
// clear conflict rather than an opaque internal error — writing nothing through it.
func TestEnsureConfig_RefusesSymlinkAtConfigPath(t *testing.T) {
	m := newInstallManager(t)

	target := filepath.Join(t.TempDir(), "victim")
	require.NoError(t, os.WriteFile(target, []byte("secret\n"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Dir(m.ConfigPath()), 0o755))
	require.NoError(t, os.Symlink(target, m.ConfigPath()))

	changed, err := m.EnsureConfig(context.Background(), baseParams())
	require.Error(t, err)
	require.False(t, changed)
	require.Equal(t, core.CodeConflict, core.CodeOf(err))

	// The symlink's target is untouched (never written through).
	data, _ := os.ReadFile(target)
	require.Equal(t, "secret\n", string(data))
}

// TestEnsureConfig_RejectsInjectionBeforeAnyWrite proves a bad value is caught at
// render time and no file is written.
func TestEnsureConfig_RejectsInjectionBeforeAnyWrite(t *testing.T) {
	m := newInstallManager(t)

	p := baseParams()
	p.AuthFile = "/x\nlisten_addr = 0.0.0.0"
	changed, err := m.EnsureConfig(context.Background(), p)
	require.Error(t, err)
	require.False(t, changed)
	require.Equal(t, core.CodeValidation, core.CodeOf(err))

	_, statErr := os.Stat(m.ConfigPath())
	require.True(t, os.IsNotExist(statErr), "no file may be written when rendering is rejected")
}

func TestEnsureConfig_RequiresRunner(t *testing.T) {
	m := New(Options{ConfDir: t.TempDir()}) // no Runner
	_, err := m.EnsureConfig(context.Background(), baseParams())
	require.Error(t, err)
	require.Equal(t, core.CodeInternal, core.CodeOf(err))
}

func TestConfigPath_DefaultsToEtcPgbouncer(t *testing.T) {
	m := New(Options{Runner: exec.NewFakeRunner()})
	require.Equal(t, "/etc/pgbouncer/pgbouncer.ini", m.ConfigPath())
}
