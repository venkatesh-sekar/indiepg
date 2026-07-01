package pg

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/indiepg/internal/exec"
)

// These tests pin the two FakeRunner-driven guards that make InstallPreflight
// "refuse to clobber an existing install": listClusters (an existing Debian
// cluster is detected → hard fail) and portListening (something already bound to
// 5432 → hard fail). Both must fail CLOSED: a probe that cannot run returns an
// error so the preflight aborts, never a silent "clean host" that lets Provision
// overwrite a live datadir. The other InstallPreflight checks (existing-dir scan,
// OS codename, free disk) read the real filesystem and are not unit-testable here.

// ---- portListening: the port-5432 clobber guard --------------------------

func TestPortListening_DetectsBoundIPv4(t *testing.T) {
	fake := exec.NewFakeRunner().On("ss -H -ltn", exec.FakeResponse{Stdout: "" +
		"LISTEN 0      4096         127.0.0.1:5432        0.0.0.0:*\n" +
		"LISTEN 0      128            0.0.0.0:22          0.0.0.0:*\n"})
	m := &Manager{runner: fake}

	busy, err := m.portListening(context.Background(), "5432")
	require.NoError(t, err)
	require.True(t, busy, "a 127.0.0.1:5432 listener must be reported as busy")
}

func TestPortListening_DetectsBoundIPv6AndWildcard(t *testing.T) {
	// Postgres commonly binds the IPv6 wildcard; the guard must catch [::]:5432
	// and *:5432 forms, not only the dotted-quad address.
	for name, addr := range map[string]string{
		"ipv6_wildcard": "[::]:5432",
		"star_wildcard": "*:5432",
		"ipv6_loopback": "[::1]:5432",
	} {
		t.Run(name, func(t *testing.T) {
			fake := exec.NewFakeRunner().On("ss -H -ltn", exec.FakeResponse{
				Stdout: "LISTEN 0      511            " + addr + "           [::]:*\n"})
			m := &Manager{runner: fake}

			busy, err := m.portListening(context.Background(), "5432")
			require.NoError(t, err)
			require.True(t, busy, "listener on %s must be reported as busy", addr)
		})
	}
}

func TestPortListening_FreeWhenNoListenerIncludingNearMissPort(t *testing.T) {
	// Several near-miss rows, none of which is a real :5432 listener. Each pins a
	// distinct one-line weakening of the match:
	//   - :15432 (port 15432) — drops the leading colon (needle = port = "5432"),
	//     which would suffix-match "...:15432".
	//   - :54321 (port 54321) — the port digits START with 5432, so the raw LINE
	//     contains the substring ":5432" even though the address token does NOT end
	//     in it; a `strings.Contains(line, needle)` rewrite (dropping the per-token
	//     suffix anchoring) would wrongly report 5432 as busy.
	//   - [fe80::5432]:22 — an IPv6 address literal that embeds "5432" while bound
	//     to :22; same Contains-vs-token-suffix trap from the address side.
	// Correct code anchors ":5432" to the end of a whitespace-delimited token, so
	// all three read as free; the loosenings turn this red.
	fake := exec.NewFakeRunner().On("ss -H -ltn", exec.FakeResponse{Stdout: "" +
		"LISTEN 0      128            0.0.0.0:22          0.0.0.0:*\n" +
		"LISTEN 0      511               [::]:80             [::]:*\n" +
		"LISTEN 0      4096        127.0.0.1:15432       0.0.0.0:*\n" +
		"LISTEN 0      4096        127.0.0.1:54321       0.0.0.0:*\n" +
		"LISTEN 0      128        [fe80::5432]:22           [::]:*\n"})
	m := &Manager{runner: fake}

	busy, err := m.portListening(context.Background(), "5432")
	require.NoError(t, err)
	require.False(t, busy, "no :5432 listener (only :22, :80, :15432, :54321, [fe80::5432]:22) must read as free")
}

func TestPortListening_FailsClosedWhenProbeErrors(t *testing.T) {
	// The security-critical invariant: an unverifiable port must NOT be declared
	// free. A swallowed `ss` error (return false, nil) would let Provision proceed
	// and clobber whatever holds 5432, so the error must propagate.
	fake := exec.NewFakeRunner().On("ss -H -ltn", exec.FakeResponse{Err: errors.New("ss: command not found")})
	m := &Manager{runner: fake}

	busy, err := m.portListening(context.Background(), "5432")
	require.Error(t, err, "an ss probe failure must surface as an error, not a silent 'free'")
	require.False(t, busy)
	require.Contains(t, err.Error(), "5432", "the error should name the port being checked")
}

// ---- listClusters: the existing-cluster clobber guard --------------------

func TestListClusters_ParsesRowsAndColumns(t *testing.T) {
	// A realistic `pg_lsclusters --no-header`: an online 16/main, a down 15/main,
	// and a defensively-dotted "17.2" Ver token. Pinning each column value turns a
	// field-index cross-wire (e.g. Port↔Status) red; the dotted row locks the
	// documented "16.4 → 16" major parse.
	fake := exec.NewFakeRunner().On("pg_lsclusters", exec.FakeResponse{Stdout: "" +
		"16   main 5432 online postgres /var/lib/postgresql/16/main /var/log/postgresql/postgresql-16-main.log\n" +
		"15   main 5433 down   postgres /var/lib/postgresql/15/main /var/log/postgresql/postgresql-15-main.log\n" +
		"17.2 main 5434 online postgres /var/lib/postgresql/17/main /var/log/postgresql/postgresql-17-main.log\n"})
	m := &Manager{runner: fake}

	clusters, err := m.listClusters(context.Background())
	require.NoError(t, err)
	require.Len(t, clusters, 3)

	require.Equal(t, 16, clusters[0].Major)
	require.Equal(t, "main", clusters[0].Name)
	require.Equal(t, "5432", clusters[0].Port)
	require.Equal(t, "online", clusters[0].Status)
	require.Equal(t, "postgres", clusters[0].Owner)
	require.Equal(t, "/var/lib/postgresql/16/main", clusters[0].DataDir)

	require.Equal(t, 15, clusters[1].Major)
	require.Equal(t, "5433", clusters[1].Port)
	require.Equal(t, "down", clusters[1].Status)

	require.Equal(t, 17, clusters[2].Major, "dotted Ver token 17.2 must parse to major 17")
}

func TestListClusters_SkipsMalformedAndBlankLinesButKeepsValid(t *testing.T) {
	// A blank line, a header-like line whose Ver is non-numeric (6 fields, so it
	// survives the length guard and is stopped only by the major==0 skip), and a
	// too-short line (2 fields — indexing it would panic without the length guard).
	// Exactly one valid cluster must survive: dropping either guard changes the
	// count (or panics) and reds the test.
	fake := exec.NewFakeRunner().On("pg_lsclusters", exec.FakeResponse{Stdout: "" +
		"\n" +
		"Ver main 5432 online postgres /var/lib/postgresql/main\n" +
		"16 main\n" +
		"16 main 5432 online postgres /var/lib/postgresql/16/main /var/log/postgresql/postgresql-16-main.log\n"})
	m := &Manager{runner: fake}

	clusters, err := m.listClusters(context.Background())
	require.NoError(t, err)
	require.Len(t, clusters, 1, "only the well-formed numeric-Ver row is a cluster")
	require.Equal(t, 16, clusters[0].Major)
}

func TestListClusters_FailsClosedWhenProbeErrors(t *testing.T) {
	// existingInstallClusters returns this error up to InstallPreflight, which
	// aborts on it. A mutation that swallowed the error into an empty slice would
	// report "no existing clusters" and let Provision clobber the datadir.
	fake := exec.NewFakeRunner().On("pg_lsclusters", exec.FakeResponse{Err: errors.New("pg_lsclusters: exit 1")})
	m := &Manager{runner: fake}

	clusters, err := m.listClusters(context.Background())
	require.Error(t, err, "a pg_lsclusters failure must not be swallowed into 'no clusters'")
	require.Nil(t, clusters)
}
