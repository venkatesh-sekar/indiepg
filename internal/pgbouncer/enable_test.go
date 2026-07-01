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
	"github.com/venkatesh-sekar/indiepg/internal/pg"
)

// a valid (synthetic) SCRAM-SHA-256 verifier shaped like pg_authid.rolpassword;
// only its charset/prefix matter to RenderUserlist.
const testVerifier = "SCRAM-SHA-256$4096:c2FsdHNhbHQ=$c3RvcmVka2V5:c2VydmVya2V5"

// fakeVerifierSource returns canned verifiers (or an error) and records the
// roles it was asked for, standing in for pg.Manager.RoleVerifiers.
type fakeVerifierSource struct {
	out    []pg.RoleVerifier
	err    error
	called [][]string
}

func (f *fakeVerifierSource) RoleVerifiers(_ context.Context, roleNames []string) ([]pg.RoleVerifier, error) {
	f.called = append(f.called, append([]string(nil), roleNames...))
	if f.err != nil {
		return nil, f.err
	}
	return f.out, nil
}

// fakeState is an in-memory PoolerState (GetConfig/SetConfig), mirroring the
// store's NotFound-on-missing contract.
type fakeState struct {
	kv      map[string]string
	setErr  error
	getErr  error
	setKeys []string
}

func newFakeState() *fakeState { return &fakeState{kv: map[string]string{}} }

func (s *fakeState) GetConfig(_ context.Context, key string) (string, error) {
	if s.getErr != nil {
		return "", s.getErr
	}
	v, ok := s.kv[key]
	if !ok {
		return "", core.NotFoundError("config key %q not set", key)
	}
	return v, nil
}

func (s *fakeState) SetConfig(_ context.Context, key, value string) error {
	if s.setErr != nil {
		return s.setErr
	}
	s.setKeys = append(s.setKeys, key)
	s.kv[key] = value
	return nil
}

// newEnableManager builds a Manager whose config/auth_file land in a temp dir so
// the atomic installers run for real without touching /etc.
func newEnableManager(t *testing.T) (*Manager, *exec.FakeRunner) {
	t.Helper()
	r := exec.NewFakeRunner()
	// is-active must report "active" so the verify step passes on the happy path.
	r.On("is-active pgbouncer", exec.FakeResponse{Stdout: "active\n"})
	// SystemdDir is a temp dir so EnsureRuntimeDir's drop-in install never touches
	// the real /etc/systemd/system.
	m := New(Options{Runner: r, Logger: core.Discard(), ConfDir: t.TempDir(), SystemdDir: t.TempDir()})
	return m, r
}

func okParams() EnableParams {
	return EnableParams{
		RoleNames:        []string{"app"},
		PGMaxConnections: 100,
		Profile:          pg.ProfileMixed,
	}
}

func okSource() *fakeVerifierSource {
	return &fakeVerifierSource{out: []pg.RoleVerifier{{Name: "app", Verifier: testVerifier}}}
}

func TestEnable_HappyPath_WiresEveryStepInOrder(t *testing.T) {
	m, r := newEnableManager(t)
	src := okSource()
	state := newFakeState()

	res, err := m.Enable(context.Background(), src, state, okParams())
	require.NoError(t, err)

	// First run writes both files, so it reloads, and the unit reports active.
	require.True(t, res.ConfigChanged)
	require.True(t, res.UserlistChanged)
	require.True(t, res.Reloaded)
	require.True(t, res.Running)
	require.Equal(t, []string{"app"}, res.PooledRoles)
	require.Equal(t, 100, res.Pool.PgMaxConnections)

	// The verifier source was asked for exactly the requested roles.
	require.Equal(t, [][]string{{"app"}}, src.called)

	// The service primitives ran in the expected relative order: install →
	// enable → reload → is-active.
	var order []string
	for _, c := range r.Calls() {
		order = append(order, argvOf(c))
	}
	joined := strings.Join(order, "\n")
	require.Contains(t, joined, "apt-get install -y pgbouncer")
	// daemon-reload (from the RuntimeDirectory= drop-in install) must land after the
	// package install and BEFORE the unit is enabled/started, so systemd has the
	// runtime-dir directive when it brings the pooler up.
	requireOrder(t, order,
		"apt-get install -y pgbouncer",
		"systemctl daemon-reload",
		"systemctl enable --now pgbouncer",
		"systemctl reload pgbouncer",
		"systemctl is-active pgbouncer",
	)

	// Both managed files were actually installed, 0640, with the config's
	// auth_file pointing at the installed userlist.
	requireFileMode(t, m.ConfigPath(), 0o640)
	requireFileMode(t, m.UserlistPath(), 0o640)
	cfg, err := os.ReadFile(m.ConfigPath())
	require.NoError(t, err)
	require.Contains(t, string(cfg), "auth_file = "+m.UserlistPath())

	// Enabled state persisted last, only after the pooler is confirmed up.
	require.Equal(t, "true", state.kv[EnabledConfigKey])
	enabled, err := IsEnabled(context.Background(), state)
	require.NoError(t, err)
	require.True(t, enabled)
}

func TestEnable_SecondRunIsIdempotentNoBounce(t *testing.T) {
	m, _ := newEnableManager(t)
	src := okSource()
	state := newFakeState()

	_, err := m.Enable(context.Background(), src, state, okParams())
	require.NoError(t, err)

	// Re-run with identical inputs: nothing changes on disk, so no reload fires.
	res2, err := m.Enable(context.Background(), src, state, okParams())
	require.NoError(t, err)
	require.False(t, res2.ConfigChanged, "unchanged config must not be rewritten")
	require.False(t, res2.UserlistChanged, "unchanged auth_file must not be rewritten")
	require.False(t, res2.Reloaded, "an unchanged pooler must never be bounced")
	require.True(t, res2.Running)
}

func TestEnable_ReloadsOnlyWhenSomethingChanged(t *testing.T) {
	m, _ := newEnableManager(t)
	state := newFakeState()

	// First enable for role "app".
	_, err := m.Enable(context.Background(), okSource(), state, okParams())
	require.NoError(t, err)

	// Adding a second role changes the auth_file (but the pool config is unchanged
	// at the same max_connections): a reload must still fire to pick up the user.
	src2 := &fakeVerifierSource{out: []pg.RoleVerifier{
		{Name: "app", Verifier: testVerifier},
		{Name: "worker", Verifier: testVerifier},
	}}
	p2 := okParams()
	p2.RoleNames = []string{"app", "worker"}
	res, err := m.Enable(context.Background(), src2, state, p2)
	require.NoError(t, err)
	require.True(t, res.UserlistChanged)
	require.False(t, res.ConfigChanged)
	require.True(t, res.Reloaded, "an auth_file change must trigger a reload")
	require.ElementsMatch(t, []string{"app", "worker"}, res.PooledRoles)
}

func TestEnable_VerifierErrorStopsBeforeWritingFiles(t *testing.T) {
	m, r := newEnableManager(t)
	src := &fakeVerifierSource{err: core.NotFoundError("role %q does not exist", "app")}
	state := newFakeState()

	_, err := m.Enable(context.Background(), src, state, okParams())
	require.Error(t, err)
	require.Equal(t, core.CodeNotFound, core.CodeOf(err))

	// No config/auth_file written, service never enabled, state never persisted.
	_, statErr := os.Stat(m.ConfigPath())
	require.True(t, os.IsNotExist(statErr))
	_, statErr = os.Stat(m.UserlistPath())
	require.True(t, os.IsNotExist(statErr))
	require.NotContains(t, allArgv(r), "systemctl enable --now pgbouncer")
	require.Empty(t, state.setKeys)
}

func TestEnable_ForeignConfigStopsBeforeWritingAuthFile(t *testing.T) {
	r := exec.NewFakeRunner()
	r.On("is-active pgbouncer", exec.FakeResponse{Stdout: "active\n"})
	dir := t.TempDir()
	m := New(Options{Runner: r, Logger: core.Discard(), ConfDir: dir})

	// A hand-written / distro pgbouncer.ini that indiepg does not own.
	foreign := []byte("[pgbouncer]\nlisten_port = 6432\n")
	require.NoError(t, os.WriteFile(m.ConfigPath(), foreign, 0o644))

	_, err := m.Enable(context.Background(), okSource(), newFakeState(), okParams())
	require.Error(t, err)
	require.Equal(t, core.CodeConflict, core.CodeOf(err))

	// The foreign config is untouched and — critically — the secret-adjacent
	// auth_file was never written into a directory indiepg does not own.
	got, readErr := os.ReadFile(m.ConfigPath())
	require.NoError(t, readErr)
	require.Equal(t, foreign, got)
	_, statErr := os.Stat(m.UserlistPath())
	require.True(t, os.IsNotExist(statErr), "auth_file must not be written when the config is foreign")
}

func TestEnable_NonScramVerifierRefusedAndNotEnabled(t *testing.T) {
	m, r := newEnableManager(t)
	// md5 verifier: RenderUserlist must refuse it (no auth downgrade).
	src := &fakeVerifierSource{out: []pg.RoleVerifier{{Name: "app", Verifier: "md5deadbeef"}}}
	state := newFakeState()

	_, err := m.Enable(context.Background(), src, state, okParams())
	require.Error(t, err)
	require.Equal(t, core.CodeValidation, core.CodeOf(err))
	require.NotContains(t, allArgv(r), "systemctl enable --now pgbouncer")
	require.Empty(t, state.setKeys)
}

func TestEnable_ServiceNotRunningAfterStartIsNotRecorded(t *testing.T) {
	r := exec.NewFakeRunner()
	// is-active reports a definitive "failed" → IsRunning returns (false, nil).
	r.On("is-active pgbouncer", exec.FakeResponse{
		Stdout: "failed\n", ExitCode: 3, Err: errors.New("exit status 3"),
	})
	m := New(Options{Runner: r, Logger: core.Discard(), ConfDir: t.TempDir(), SystemdDir: t.TempDir()})

	res, err := m.Enable(context.Background(), okSource(), newFakeState(), okParams())
	require.Error(t, err)
	// The first enable always writes both files, so Reload runs and its own
	// post-apply verification catches the dead pooler first (CodeExec) — before
	// the flow's later IsRunning gate. Either way the pooler is confirmed down and
	// nothing is recorded; the contract that matters is that Enable does not
	// report success over a pooler that never came up.
	require.Equal(t, core.CodeExec, core.CodeOf(err))
	require.False(t, res.Running)
}

// TestEnable_NoConfigChangeReRunOverDeadPoolerFailsAtFinalGate covers the OTHER
// dead-pooler catch point: the flow's final IsRunning gate (enable.go:213-216).
// On an idempotent re-run where nothing changed, Reload is skipped entirely, so
// that final gate is the ONLY thing standing between a pooler that has since
// died and the code persisting enabled=true — which would make the panel report
// a live pooler over a dead one. TestEnable_ServiceNotRunningAfterStartIsNotRecorded
// only exercises the first-run path, where Reload's own post-apply verify fires
// first; deleting the final gate leaves that test (and the whole suite) green.
func TestEnable_NoConfigChangeReRunOverDeadPoolerFailsAtFinalGate(t *testing.T) {
	m, r := newEnableManager(t)
	state := newFakeState()

	// First enable succeeds on a healthy pooler: both files are written, the unit
	// reports active, and enabled=true is persisted.
	res1, err := m.Enable(context.Background(), okSource(), state, okParams())
	require.NoError(t, err)
	require.True(t, res1.Running)
	require.Equal(t, "true", state.kv[EnabledConfigKey])
	persistsAfterFirst := len(state.setKeys)

	// The pooler then dies out from under us. A definitive "inactive" (no runner
	// error) makes IsRunning return a clean (false, nil), so the failure can only
	// be caught by the flow's own gate, not by an IsRunning transport error.
	r.On("is-active pgbouncer", exec.FakeResponse{Stdout: "inactive\n"})

	// Re-run with identical inputs: nothing changes on disk, so Reload is skipped.
	// The final IsRunning gate must catch the dead pooler.
	res2, err := m.Enable(context.Background(), okSource(), state, okParams())
	require.Error(t, err)

	// Proof it was the FINAL gate (not Reload, not an IsRunning transport error):
	//   - Reload was skipped, so nothing changed on this run.
	require.False(t, res2.ConfigChanged, "an unchanged config must not be rewritten")
	require.False(t, res2.UserlistChanged, "an unchanged auth_file must not be rewritten")
	require.False(t, res2.Reloaded, "no reload may fire when nothing changed")
	//   - The final gate reports CodeInternal ("did not come up after enable");
	//     Reload's dead-pooler error is CodeExec ("not running afterward") and
	//     IsRunning's transport error is CodeExec ("could not determine state"),
	//     so this uniquely pins the enable.go:213 gate.
	require.Equal(t, core.CodeInternal, core.CodeOf(err))
	require.ErrorContains(t, err, "did not come up after enable")
	require.False(t, res2.Running, "a dead pooler must be reported down, never up")

	// The gate short-circuits before the persist step, so enabled is NOT
	// re-written over a dead pooler — SetConfig is not called again.
	require.Len(t, state.setKeys, persistsAfterFirst,
		"the final gate must return before persisting when the pooler is down")
}

// TestEnable_NoConfigChangeReRunUndeterminableStateFailsAtFinalGate is the
// error-arm counterpart to the test above. The flow's final gate (enable.go:208)
// has TWO ways to catch a bad pooler on the no-change re-run: `if !running`
// (definitive dead, covered above) AND `if err != nil` — IsRunning could not even
// ask systemctl, so the state is genuinely UNKNOWN. That "couldn't ask" arm must
// surface loudly, never be swallowed. IsRunning returns (false, err) only when
// stdout is empty AND the runner errored, so this drives exactly that branch.
// Without this test, dropping IsRunning's error return (`running, _ :=
// m.IsRunning(ctx)`) would silently downgrade "unknown" to the definitive "did
// not come up" — a regression the definitive-dead test cannot catch, since it
// never exercises the error branch.
func TestEnable_NoConfigChangeReRunUndeterminableStateFailsAtFinalGate(t *testing.T) {
	m, r := newEnableManager(t)
	state := newFakeState()

	// First enable succeeds on a healthy pooler.
	res1, err := m.Enable(context.Background(), okSource(), state, okParams())
	require.NoError(t, err)
	require.True(t, res1.Running)
	persistsAfterFirst := len(state.setKeys)

	// systemctl itself can no longer be reached: an EMPTY stdout paired with a
	// runner error is the only shape that makes IsRunning return (false, err)
	// rather than a definitive (false, nil), so this exercises the gate's
	// error-propagation arm and not its `!running` arm.
	r.On("is-active pgbouncer", exec.FakeResponse{Err: errors.New("systemctl: command not found")})

	// Re-run with identical inputs: nothing changes on disk, so Reload is skipped
	// and the final IsRunning gate is the only thing that can react.
	res2, err := m.Enable(context.Background(), okSource(), state, okParams())
	require.Error(t, err)
	require.False(t, res2.Reloaded, "no reload may fire when nothing changed")

	// The gate must propagate IsRunning's "couldn't ask" error verbatim (CodeExec,
	// "could not determine service state") — NOT downgrade it to the definitive
	// "did not come up after enable" (CodeInternal). If IsRunning's error return
	// were dropped, the flow would report the latter instead, failing here.
	require.Equal(t, core.CodeExec, core.CodeOf(err))
	require.ErrorContains(t, err, "could not determine service state")
	require.NotContains(t, err.Error(), "did not come up after enable",
		"an unknown state must not be reported as a definitive down")
	require.False(t, res2.Running, "an undeterminable state must never report the pooler up")

	// An unknown state must not persist enabled=true either — no re-persist.
	require.Len(t, state.setKeys, persistsAfterFirst,
		"an undeterminable pooler state must not persist enabled=true")
}

func TestEnable_DoesNotPersistWhenBringUpFails(t *testing.T) {
	r := exec.NewFakeRunner()
	r.On("enable --now pgbouncer", exec.FakeResponse{Err: errors.New("unit not found")})
	// Register is-active too so only `enable --now` fails — the error code then
	// reflects that step, not an incidental later one if the match ever shifted.
	r.On("is-active pgbouncer", exec.FakeResponse{Stdout: "active\n"})
	m := New(Options{Runner: r, Logger: core.Discard(), ConfDir: t.TempDir(), SystemdDir: t.TempDir()})
	state := newFakeState()

	_, err := m.Enable(context.Background(), okSource(), state, okParams())
	require.Error(t, err)
	require.Equal(t, core.CodeExec, core.CodeOf(err))
	require.Empty(t, state.setKeys, "enabled flag must not be set when the service fails to start")
}

func TestEnable_PersistFailureSurfaces(t *testing.T) {
	m, _ := newEnableManager(t)
	state := newFakeState()
	state.setErr = errors.New("db locked")

	_, err := m.Enable(context.Background(), okSource(), state, okParams())
	require.Error(t, err)
}

func TestEnable_ValidatesInputs(t *testing.T) {
	m, _ := newEnableManager(t)

	// no roles
	_, err := m.Enable(context.Background(), okSource(), newFakeState(), EnableParams{PGMaxConnections: 100})
	require.Equal(t, core.CodeValidation, core.CodeOf(err))

	// non-positive max_connections
	p := okParams()
	p.PGMaxConnections = 0
	_, err = m.Enable(context.Background(), okSource(), newFakeState(), p)
	require.Equal(t, core.CodeValidation, core.CodeOf(err))

	// missing collaborators
	_, err = m.Enable(context.Background(), nil, newFakeState(), okParams())
	require.Equal(t, core.CodeInternal, core.CodeOf(err))
	_, err = m.Enable(context.Background(), okSource(), nil, okParams())
	require.Equal(t, core.CodeInternal, core.CodeOf(err))

	mNoRunner := New(Options{Logger: core.Discard()})
	_, err = mNoRunner.Enable(context.Background(), okSource(), newFakeState(), okParams())
	require.Equal(t, core.CodeInternal, core.CodeOf(err))
}

func TestIsEnabled_DefaultsOffWhenUnset(t *testing.T) {
	state := newFakeState()
	enabled, err := IsEnabled(context.Background(), state)
	require.NoError(t, err)
	require.False(t, enabled, "an unset key is the default-off state, not an error")

	// A non-NotFound read error must surface.
	state.getErr = errors.New("db unreadable")
	_, err = IsEnabled(context.Background(), state)
	require.Error(t, err)
}

// --- helpers ---

func allArgv(r *exec.FakeRunner) string {
	var b strings.Builder
	for _, c := range r.Calls() {
		b.WriteString(argvOf(c))
		b.WriteByte('\n')
	}
	return b.String()
}

func requireFileMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	fi, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, want, fi.Mode().Perm(), "unexpected mode on %s", filepath.Base(path))
}

// requireOrder asserts that want appears as a subsequence of got (relative order
// preserved; other calls may interleave).
func requireOrder(t *testing.T, got []string, want ...string) {
	t.Helper()
	i := 0
	for _, g := range got {
		if i < len(want) && g == want[i] {
			i++
		}
	}
	require.Equal(t, len(want), i, "calls %v did not contain ordered subsequence %v", got, want)
}
