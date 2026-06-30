package backup

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/exec"
)

// recordingCluster is a backup.ClusterController that appends "stop"/"start" to a
// shared ordered log, so a test can assert the stop -> restore -> start sequence,
// and can be made to fail either call to exercise the safety paths.
type recordingCluster struct {
	events   *[]string
	stopErr  error
	startErr error
}

func (c *recordingCluster) StopCluster(context.Context) error {
	*c.events = append(*c.events, "stop")
	return c.stopErr
}

func (c *recordingCluster) StartCluster(context.Context) error {
	*c.events = append(*c.events, "start")
	return c.startErr
}

// orderRunner wraps a FakeRunner and appends "restore" to a shared ordered log
// when the pgBackRest restore command runs, so the test can interleave the
// runner's restore against the cluster controller's stop/start and assert order.
type orderRunner struct {
	inner  *exec.FakeRunner
	events *[]string
}

func (o *orderRunner) Run(ctx context.Context, spec exec.RunSpec) (exec.RunResult, error) {
	joined := strings.Join(append([]string{spec.Name}, spec.Args...), " ")
	// The restore subcommand is unique to RestoreCmd (backup/info never carry it).
	if strings.Contains(joined, "restore") {
		*o.events = append(*o.events, "restore")
	}
	return o.inner.Run(ctx, spec)
}

func (o *orderRunner) DryRun() bool { return o.inner.DryRun() }

// newRestoreManager builds a Manager whose safety backup + info succeed, wired to
// the given cluster controller and an order-recording runner that shares events.
// restoreErr (when non-nil) makes the pgBackRest restore command fail. The repo
// is local-only (no bucket/owner) so the safety backup proceeds without a guard.
func newRestoreManager(t *testing.T, events *[]string, cluster ClusterController, restoreErr error) (*Manager, *exec.FakeRunner) {
	t.Helper()
	fake := exec.NewFakeRunner()
	fake.On("backup", exec.FakeResponse{Stdout: "completed"})
	fake.On("info", exec.FakeResponse{Stdout: sampleInfoJSON})
	if restoreErr != nil {
		fake.On("restore", exec.FakeResponse{ExitCode: 1, Err: restoreErr})
	} else {
		fake.On("restore", exec.FakeResponse{Stdout: "restored"})
	}
	m := New(Options{
		Runner:  &orderRunner{inner: fake, events: events},
		Store:   newTestStore(t),
		Config:  testConfig(),
		Logger:  core.Discard(),
		Cluster: cluster,
	})
	return m, fake
}

// TestRestore_StopsRestoresStarts proves the happy path stops the live cluster,
// runs the restore, then starts it again — in that order.
func TestRestore_StopsRestoresStarts(t *testing.T) {
	events := &[]string{}
	m, _ := newRestoreManager(t, events, &recordingCluster{events: events}, nil)

	res, err := m.Restore(context.Background(), nil, true, "main")
	require.NoError(t, err)
	require.True(t, res.OK)
	require.Equal(t, []string{"stop", "restore", "start"}, *events,
		"restore must stop the cluster, restore, then start it — in that order")
}

// TestRestore_StopFailureAbortsBeforeRestore proves a failed STOP is a hard stop:
// the restore is never issued and the cluster is never (re)started — the live
// cluster is left untouched and still running.
func TestRestore_StopFailureAbortsBeforeRestore(t *testing.T) {
	events := &[]string{}
	cluster := &recordingCluster{events: events, stopErr: core.ExecError("stop boom")}
	m, fake := newRestoreManager(t, events, cluster, nil)

	_, err := m.Restore(context.Background(), nil, true, "main")
	require.Error(t, err)
	require.Equal(t, []string{"stop"}, *events,
		"a failed stop must abort before the restore and not attempt a start")

	for _, c := range fake.Calls() {
		require.NotContains(t, strings.Join(c.Args, " "), "restore",
			"no pgBackRest restore may run once the stop failed")
	}
}

// TestRestore_RestoreFailureStillStarts proves that when the restore itself fails,
// the cluster is still started again so the box is never left down — and the
// restore error is surfaced.
func TestRestore_RestoreFailureStillStarts(t *testing.T) {
	events := &[]string{}
	m, _ := newRestoreManager(t, events, &recordingCluster{events: events}, core.ExecError("restore boom"))

	_, err := m.Restore(context.Background(), nil, true, "main")
	require.Error(t, err)
	require.Equal(t, []string{"stop", "restore", "start"}, *events,
		"a failed restore must still start the cluster so it is never left down")
}

// TestRestore_StartFailureAfterSuccessfulRestore proves a START failure after a
// successful restore surfaces as an error (the operator must fix the down box).
func TestRestore_StartFailureAfterSuccessfulRestore(t *testing.T) {
	events := &[]string{}
	cluster := &recordingCluster{events: events, startErr: core.ExecError("start boom")}
	m, _ := newRestoreManager(t, events, cluster, nil)

	_, err := m.Restore(context.Background(), nil, true, "main")
	require.Error(t, err)
	require.Equal(t, []string{"stop", "restore", "start"}, *events)
}

// TestRestore_NoClusterControllerRunsDirectly proves the legacy path: with no
// cluster controller wired, Restore runs the pgBackRest restore directly (no
// stop/start), preserving behaviour for local-only/test use.
func TestRestore_NoClusterControllerRunsDirectly(t *testing.T) {
	events := &[]string{}
	m, fake := newRestoreManager(t, events, nil, nil)

	res, err := m.Restore(context.Background(), nil, true, "main")
	require.NoError(t, err)
	require.True(t, res.OK)
	require.Equal(t, []string{"restore"}, *events,
		"with no cluster controller the restore runs directly")

	var ranRestore bool
	for _, c := range fake.Calls() {
		if strings.Contains(strings.Join(c.Args, " "), "restore") {
			ranRestore = true
		}
	}
	require.True(t, ranRestore, "the pgBackRest restore must still run")
}
