package pg

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/exec"
)

func TestVersionCatalog(t *testing.T) {
	require.Equal(t, 17, DefaultMajor())
	require.True(t, IsSupported(16))
	require.False(t, IsSupported(14))

	newer := MajorsNewerThan(16)
	require.Len(t, newer, 1)
	require.Equal(t, 17, newer[0].Major)
	require.True(t, newer[0].Default)

	require.Empty(t, MajorsNewerThan(17))
}

func TestParseAptPolicy(t *testing.T) {
	out := `postgresql-16:
  Installed: 16.2-1.pgdg120+2
  Candidate: 16.4-1.pgdg120+1
  Version table:
 *** 16.2-1.pgdg120+2 100`
	installed, candidate := parseAptPolicy(out)
	require.Equal(t, "16.2-1.pgdg120+2", installed)
	require.Equal(t, "16.4-1.pgdg120+1", candidate)
	require.Equal(t, "16.4", upstreamVersion(candidate))
}

func TestMinorUpdateAvailableUsesDebianVersionOrdering(t *testing.T) {
	runner := exec.NewFakeRunner()
	runner.On("apt-cache policy postgresql-16", exec.FakeResponse{Stdout: `Installed: 16.4-1.pgdg120+2
Candidate: 16.3-1.pgdg120+3`})
	runner.On("dpkg --compare-versions", exec.FakeResponse{ExitCode: 1, Err: errors.New("comparison is false")})
	m := New(Options{Runner: runner})

	available, target, err := m.MinorUpdateAvailable(context.Background(), 16)
	require.NoError(t, err)
	require.False(t, available, "an older pinned candidate is not an update")
	require.Empty(t, target)

	runner.On("apt-cache policy postgresql-16", exec.FakeResponse{Stdout: `Installed: 1:16.4-1.pgdg120+2
Candidate: 1:16.4-1.pgdg120+3`})
	runner.On("dpkg --compare-versions", exec.FakeResponse{})
	available, target, err = m.MinorUpdateAvailable(context.Background(), 16)
	require.NoError(t, err)
	require.True(t, available, "a newer Debian revision still carries fixes")
	require.Equal(t, "16.4", target)
}

func TestResolveInstallMajorRejectsUnsupported(t *testing.T) {
	m := New(Options{PGMajor: 999})
	_, err := m.resolveInstallMajor()
	require.Error(t, err)
	require.Equal(t, core.CodeValidation, core.CodeOf(err))
}

func TestUpgradeClusterPinsRequestedTarget(t *testing.T) {
	runner := exec.NewFakeRunner()
	runner.On("pg_lsclusters --no-header", exec.FakeResponse{Stdout: "16 main 5433 down postgres /var/lib/postgresql/16/main /var/log/postgresql/old.log\n17 main 5432 online postgres /var/lib/postgresql/17/main /var/log/postgresql/new.log\n"})
	m := New(Options{Runner: runner})

	got, err := m.UpgradeCluster(context.Background(), 16, 17)
	require.NoError(t, err)
	require.Equal(t, "5433", got.OldPort)
	require.Equal(t, "/var/lib/postgresql/16/main", got.OldDataDir)

	var command string
	for _, call := range runner.Calls() {
		if call.Name == "pg_upgradecluster" {
			command = call.Name + " " + strings.Join(call.Args, " ")
		}
	}
	require.Equal(t, "pg_upgradecluster --method=upgrade -v 17 16 main", command)
}

func TestRollbackRejectsUnexpectedClusterStateBeforeChangingFiles(t *testing.T) {
	runner := exec.NewFakeRunner()
	runner.On("pg_lsclusters --no-header", exec.FakeResponse{Stdout: "16 main 5433 online postgres /var/lib/postgresql/16/main old.log\n17 main 5432 online postgres /var/lib/postgresql/17/main new.log\n"})
	m := New(Options{Runner: runner})

	_, err := m.RollbackUpgrade(context.Background(), 16, 17, "5433")
	require.Error(t, err)
	require.Equal(t, core.CodeConflict, core.CodeOf(err))
	require.Len(t, runner.Calls(), 1, "validation must happen before stop or config writes")
}

func TestParseOSReleaseCodename(t *testing.T) {
	debian := `PRETTY_NAME="Debian GNU/Linux 12 (bookworm)"
VERSION_CODENAME=bookworm
ID=debian`
	require.Equal(t, "bookworm", parseOSReleaseCodename(debian))

	ubuntu := `ID=ubuntu
UBUNTU_CODENAME=jammy`
	require.Equal(t, "jammy", parseOSReleaseCodename(ubuntu))

	require.Equal(t, "", parseOSReleaseCodename("ID=weird\n"))
}

func TestRequiredExtensionPackage(t *testing.T) {
	pkg, templated := requiredExtensionPackage("vector", 17)
	require.Equal(t, "postgresql-17-pgvector", pkg)
	require.True(t, templated)

	// contrib modules ship bundled inside postgresql-17; there is no installable
	// postgresql-17-contrib, so a contrib-family extension resolves to the server
	// package (and templated=false means the caller skips a redundant install).
	pkg, templated = requiredExtensionPackage("pg_stat_statements", 17)
	require.Equal(t, "postgresql-17", pkg)
	require.False(t, templated)

	pkg, templated = requiredExtensionPackage("some_unknown_ext", 17)
	require.Equal(t, "", pkg)
	require.False(t, templated)
}

func TestCheckSetHasFail(t *testing.T) {
	cs := CheckSet{
		pass("a", "A", "ok"),
		warn("b", "B", "careful", "hint"),
	}
	require.False(t, cs.HasFail())

	cs = append(cs, fail("c", "C", "blocked", "fix it"))
	require.True(t, cs.HasFail())
}

// memStateStore is an in-memory pg.StateStore for the round-trip test. A missing
// key returns a CodeNotFound error, matching the *store.Store contract.
type memStateStore struct{ m map[string]string }

func (s *memStateStore) GetConfig(_ context.Context, key string) (string, error) {
	if s.m == nil {
		s.m = map[string]string{}
	}
	v, ok := s.m[key]
	if !ok {
		return "", core.NotFoundError("config key %q not set", key)
	}
	return v, nil
}

func (s *memStateStore) SetConfig(_ context.Context, key, value string) error {
	if s.m == nil {
		s.m = map[string]string{}
	}
	s.m[key] = value
	return nil
}

func (s *memStateStore) DeleteConfig(_ context.Context, key string) error {
	delete(s.m, key)
	return nil
}

func TestUpgradeStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	us := NewUpgradeStore(&memStateStore{})

	// A fresh store yields a zero (non-nil) state with no pending finalization.
	st, err := us.Load(ctx)
	require.NoError(t, err)
	require.NotNil(t, st)
	require.Nil(t, st.Pending)

	// Mutate to record a pending finalization and an operation, then reload it.
	_, err = us.Mutate(ctx, func(st *UpgradeState) {
		st.Pending = &PendingFinalization{FromMajor: 16, ToMajor: 17, ReclaimableBytes: 1234}
		st.OldClusterPort = "5433"
		st.Operation = &OperationState{Kind: OpMajor, Status: OpStatusSuccess, TargetMajor: 17}
	})
	require.NoError(t, err)

	got, err := us.Load(ctx)
	require.NoError(t, err)
	require.NotNil(t, got.Pending)
	require.Equal(t, 16, got.Pending.FromMajor)
	require.Equal(t, 17, got.Pending.ToMajor)
	require.Equal(t, int64(1234), got.Pending.ReclaimableBytes)
	require.Equal(t, "5433", got.OldClusterPort)
	require.NotNil(t, got.Operation)
	require.Equal(t, OpMajor, got.Operation.Kind)
}
