package backup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/exec"
)

func newProvisionManager(t *testing.T, runner exec.Runner) *Manager {
	t.Helper()
	dir := t.TempDir()
	return New(Options{Runner: runner, Logger: core.Discard(), ConfDir: dir})
}

func TestEnsureConfigured_WritesFileAndCreatesStanza(t *testing.T) {
	runner := exec.NewFakeRunner().On("stanza-create", exec.FakeResponse{Stdout: "ok"})
	m := newProvisionManager(t, runner)

	changed, err := m.EnsureConfigured(context.Background(), baseS3Params())
	require.NoError(t, err)
	require.True(t, changed)

	// File exists, carries the marker and the secret, and is 0600.
	data, err := os.ReadFile(m.ConfigPath())
	require.NoError(t, err)
	require.True(t, HasManagedMarker(string(data)))
	require.Contains(t, string(data), "repo1-s3-key-secret=s3cr3t/with+base64=chars")

	info, err := os.Stat(m.ConfigPath())
	require.NoError(t, err)
	require.Equal(t, confFileMode, info.Mode().Perm(), "config must be 0600 (carries the S3 secret)")

	// stanza-create ran, as the postgres user, pointed at our config file.
	calls := runner.Calls()
	require.Len(t, calls, 1)
	require.Equal(t, "pgbackrest", calls[0].Name)
	require.Equal(t, "postgres", calls[0].AsUser)
	require.Contains(t, calls[0].Args, "stanza-create")
	require.Contains(t, calls[0].Args, "--config="+m.ConfigPath())
	require.Contains(t, calls[0].Args, "--stanza=main")
}

func TestEnsureConfigured_IdempotentNoRewriteNoStanzaCreate(t *testing.T) {
	runner := exec.NewFakeRunner().On("stanza-create", exec.FakeResponse{Stdout: "ok"})
	m := newProvisionManager(t, runner)

	changed, err := m.EnsureConfigured(context.Background(), baseS3Params())
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, 1, runner.CallCount())

	// Second identical call: no change, so no second stanza-create.
	changed, err = m.EnsureConfigured(context.Background(), baseS3Params())
	require.NoError(t, err)
	require.False(t, changed, "unchanged config must be a no-op")
	require.Equal(t, 1, runner.CallCount(), "stanza-create must not re-run when config is unchanged")
}

func TestEnsureConfigured_RewritesWhenChanged(t *testing.T) {
	runner := exec.NewFakeRunner().On("stanza-create", exec.FakeResponse{Stdout: "ok"})
	m := newProvisionManager(t, runner)

	_, err := m.EnsureConfigured(context.Background(), baseS3Params())
	require.NoError(t, err)

	p := baseS3Params()
	p.RetentionDays = 30
	changed, err := m.EnsureConfigured(context.Background(), p)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, 2, runner.CallCount())
	data, _ := os.ReadFile(m.ConfigPath())
	require.Contains(t, string(data), "repo1-retention-full=30")
}

// TestEnsureConfigured_RefusesForeignConfig is the safety-critical case: a config
// file indiepg did NOT write (no marker) must never be clobbered.
func TestEnsureConfigured_RefusesForeignConfig(t *testing.T) {
	runner := exec.NewFakeRunner().On("stanza-create", exec.FakeResponse{Stdout: "ok"})
	m := newProvisionManager(t, runner)

	// Pre-place an operator's hand-written config.
	require.NoError(t, os.MkdirAll(filepath.Dir(m.ConfigPath()), 0o755))
	foreign := "[global]\nrepo1-type=s3\n# my own config\n"
	require.NoError(t, os.WriteFile(m.ConfigPath(), []byte(foreign), 0o600))

	_, err := m.EnsureConfigured(context.Background(), baseS3Params())
	require.Error(t, err)
	require.Equal(t, core.CodeConflict, core.CodeOf(err))
	require.Equal(t, 0, runner.CallCount(), "must not run stanza-create when refusing to overwrite")

	// The operator's file is untouched.
	data, _ := os.ReadFile(m.ConfigPath())
	require.Equal(t, foreign, string(data))
}

func TestEnsureConfigured_PropagatesStanzaCreateFailure(t *testing.T) {
	runner := exec.NewFakeRunner().On("stanza-create", exec.FakeResponse{
		ExitCode: 1, Stderr: "ERROR: unable to reach repo", Err: os.ErrPermission,
	})
	m := newProvisionManager(t, runner)

	_, err := m.EnsureConfigured(context.Background(), baseS3Params())
	require.Error(t, err)
	require.Equal(t, core.CodeExec, core.CodeOf(err))
	// The config file is still written (so a retry sees it); the error is the run.
	_, statErr := os.Stat(m.ConfigPath())
	require.NoError(t, statErr)
}

func TestEnsureConfigured_RejectsInjectionBeforeAnyWrite(t *testing.T) {
	runner := exec.NewFakeRunner().On("stanza-create", exec.FakeResponse{Stdout: "ok"})
	m := newProvisionManager(t, runner)

	p := baseS3Params()
	p.SecretKey = "x\nrepo1-path=/evil"
	_, err := m.EnsureConfigured(context.Background(), p)
	require.Error(t, err)
	require.Equal(t, core.CodeValidation, core.CodeOf(err))
	require.Equal(t, 0, runner.CallCount())
	_, statErr := os.Stat(m.ConfigPath())
	require.True(t, os.IsNotExist(statErr), "no file may be written when rendering is rejected")
}

func TestEnsureConfigured_DoesNotLeakSecretIntoStanzaArgv(t *testing.T) {
	runner := exec.NewFakeRunner().On("stanza-create", exec.FakeResponse{Stdout: "ok"})
	m := newProvisionManager(t, runner)

	_, err := m.EnsureConfigured(context.Background(), baseS3Params())
	require.NoError(t, err)

	for _, c := range runner.Calls() {
		joined := strings.Join(append([]string{c.Name}, c.Args...), " ")
		require.NotContains(t, joined, "s3cr3t", "the S3 secret must never appear in a command argv")
	}
}
