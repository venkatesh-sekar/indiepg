package pg

import (
	"context"
	"errors"
	"strings"
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

// ---- MajorUpgradePreflight's psql-scraping blocker substrate -------------
//
// MajorUpgradePreflight gates a destructive major upgrade on a handful of
// psql-scraped facts. The full preflight can't be unit-driven here (its
// target-binary and free-disk checks read hardcoded absolute paths with no
// injectable FS seam — the same residual documented for InstallPreflight), but
// the runner-driven substrate it stands on can and must be pinned: scalarCount
// (the prepared-transaction / logical-slot blockers) and installedExtensions
// (the extension-parity requirement + upgrade preview). Both feed hard blockers,
// so both must FAIL LOUD rather than return a clean-looking zero/partial value
// that would let pg_upgrade run into a state it can't handle.

// scalarCount backs two upgrade blockers:
//
//	if n, err := m.scalarCount(ctx, "... pg_prepared_xacts");     err != nil -> fail("could not verify")
//	                                                              else n > 0  -> fail("N prepared transactions ...")
//	if n, err := m.scalarCount(ctx, "... pg_replication_slots");  ... (same shape)
//
// So an unparseable/empty result MUST surface an error. A silent (0, nil) would
// read as "no blocker → pass" and let pg_upgrade proceed with open prepared
// transactions or logical slots it cannot carry.

func TestScalarCount_ParsesInteger(t *testing.T) {
	for name, tc := range map[string]struct {
		stdout string
		want   int
	}{
		"zero":              {"0\n", 0},
		"positive":          {"3\n", 3},
		"surrounding_space": {"  2 \n", 2}, // pins the TrimSpace before Atoi
	} {
		t.Run(name, func(t *testing.T) {
			fake := exec.NewFakeRunner().On("pg_prepared_xacts", exec.FakeResponse{Stdout: tc.stdout})
			m := &Manager{runner: fake}

			n, err := m.scalarCount(context.Background(), "SELECT count(*) FROM pg_prepared_xacts")
			require.NoError(t, err)
			require.Equal(t, tc.want, n)
		})
	}
}

func TestScalarCount_FailsLoudOnUnparseableOutput(t *testing.T) {
	// The fail-loud invariant. `return 0, nil` on the parse-error branch would
	// defeat BOTH the prepared-transaction and logical-slot blockers at once.
	for name, stdout := range map[string]string{
		"empty":   "",               // query returned no row at all
		"blank":   "   \n",          // whitespace only -> TrimSpace to ""
		"garbage": "not-a-number\n", // non-numeric scalar
	} {
		t.Run(name, func(t *testing.T) {
			fake := exec.NewFakeRunner().On("pg_prepared_xacts", exec.FakeResponse{Stdout: stdout})
			m := &Manager{runner: fake}

			n, err := m.scalarCount(context.Background(), "SELECT count(*) FROM pg_prepared_xacts")
			require.Error(t, err, "unparseable count output must surface an error, not a silent 0")
			require.Zero(t, n)
			require.Contains(t, err.Error(), "parsing count", "the error should name what it failed to parse")
		})
	}
}

func TestScalarCount_PropagatesPsqlError(t *testing.T) {
	// A psql/connection failure must propagate, not read as a clean 0 — same
	// blocker-defeat hazard as an unparseable count.
	fake := exec.NewFakeRunner().On("pg_prepared_xacts",
		exec.FakeResponse{Err: errors.New("psql: could not connect to server")})
	m := &Manager{runner: fake}

	n, err := m.scalarCount(context.Background(), "SELECT count(*) FROM pg_prepared_xacts")
	require.Error(t, err)
	require.Zero(t, n)
}

// installedExtensions discovers the extensions installed across every database.
// Its output drives the extension-parity blocker (an extension with no
// target-major build fails the upgrade) and the preview, so it must union +
// dedup across databases, drop blank lines, and — critically — FAIL LOUD if any
// per-database query errors instead of returning a partial set that would
// silently under-report the parity requirement and miss a missing-build blocker.
// (The plpgsql exclusion is enforced in the SQL itself: `extname <> 'plpgsql'`.)

func TestInstalledExtensions_DedupsAndSortsAcrossDatabases(t *testing.T) {
	// pg_trgm is present in both databases (must be counted once) and an interior
	// blank line must not become an empty extension.
	fake := exec.NewFakeRunner().
		On("datname FROM pg_database", exec.FakeResponse{Stdout: "app\nshop\n"}).
		On("-d app -c", exec.FakeResponse{Stdout: "vector\n\npg_trgm\n"}).
		On("-d shop -c", exec.FakeResponse{Stdout: "pg_trgm\npostgis\n"})
	m := &Manager{runner: fake}

	exts, err := m.installedExtensions(context.Background())
	require.NoError(t, err)
	require.Len(t, exts, 3, "pg_trgm appears in both databases but must be de-duplicated to one entry")
	require.NotContains(t, exts, "", "a blank line must not be recorded as an empty extension")
	require.Equal(t, []string{"pg_trgm", "postgis", "vector"}, exts, "sorted, de-duplicated union across databases")

	// The plpgsql exclusion is enforced in the query text, which the FakeRunner
	// does not execute — so pin the predicate on the recorded argv. Dropping the
	// WHERE (reports the always-present plpgsql as an installed extension → a bogus
	// versioned-package demand + polluted preview) or inverting it to
	// `= 'plpgsql'` (hides every real extension from the parity blocker, silently
	// letting a destructive pg_upgrade proceed) must red this. Counting the queries
	// also pins one-per-database, so a mutation that skips a database is caught.
	extQueries := 0
	for _, c := range fake.Calls() {
		joined := strings.Join(c.Args, " ")
		if strings.Contains(joined, "FROM pg_extension") {
			extQueries++
			require.Contains(t, joined, "extname <> 'plpgsql'",
				"the extension query must exclude the always-present plpgsql, else parity mis-reports")
		}
	}
	require.Equal(t, 2, extQueries, "one extension query per database (app, shop)")
}

func TestInstalledExtensions_FailsLoudOnPerDatabaseError(t *testing.T) {
	// The second database's extension query fails. installedExtensions must return
	// that error — NOT a partial ["vector"] set that would let the parity check
	// under-report — and the error must name the failing database.
	fake := exec.NewFakeRunner().
		On("datname FROM pg_database", exec.FakeResponse{Stdout: "app\nshop\n"}).
		On("-d app -c", exec.FakeResponse{Stdout: "vector\n"}).
		On("-d shop -c", exec.FakeResponse{Err: errors.New(`psql: FATAL: database "shop" does not exist`)})
	m := &Manager{runner: fake}

	exts, err := m.installedExtensions(context.Background())
	require.Error(t, err, "a per-database psql failure must not be swallowed into a partial set")
	require.Nil(t, exts)
	require.Contains(t, err.Error(), "shop", "the error should name the database whose extension query failed")
}

func TestInstalledExtensions_FailsLoudWhenDatabaseListErrors(t *testing.T) {
	// If enumerating the databases itself fails (via listDatabaseNames), the error
	// must propagate rather than yield an empty "no extensions" set.
	fake := exec.NewFakeRunner().
		On("datname FROM pg_database", exec.FakeResponse{Err: errors.New("psql: could not connect to server")})
	m := &Manager{runner: fake}

	exts, err := m.installedExtensions(context.Background())
	require.Error(t, err)
	require.Nil(t, exts)
}
