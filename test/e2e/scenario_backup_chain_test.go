//go:build e2e

package e2e

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/indiepg/test/e2e/harness"
)

// pgbackrestStanza / pgbackrestBackup mirror the fields of `pgbackrest info
// --output=json` the chain assertion reads. The info payload is a JSON array of
// stanzas; each stanza carries a "backup" array whose entries record their type
// and their dependency on prior backups (prior = immediate parent label,
// reference = the full set of prior labels needed to restore).
type pgbackrestBackup struct {
	Label     string   `json:"label"`
	Type      string   `json:"type"` // full | incr | diff
	Prior     string   `json:"prior"`
	Reference []string `json:"reference"`
}

type pgbackrestStanza struct {
	Name   string             `json:"name"`
	Backup []pgbackrestBackup `json:"backup"`
}

func referencesLabel(refs []string, label string) bool {
	for _, r := range refs {
		if r == label {
			return true
		}
	}
	return false
}

// TestBackupChain is scenario 3: a full -> incr -> diff pgBackRest backup chain
// to MinIO on the preinstalled image. It drives the panel's real backup flow for
// all three types and asserts the chain two ways: (a) the pgBackRest repo's own
// `info` metadata shows exactly one full plus a dependent incr and diff that each
// reference the full, and (b) the MinIO object count under backup/main grows
// monotonically as each link is added.
func TestBackupChain(t *testing.T) {
	t.Parallel()

	env := harness.Up(t, harness.Options{Image: harness.ImagePreinstalled})
	require.NoError(t, env.Panel.Login(harness.AdminPassword))

	// Configure the S3 target against MinIO. BackupConfigured=true means the panel
	// rendered the pgBackRest config, turned on WAL archiving, and ran stanza-create.
	cfgResp, err := env.Panel.ConfigureS3(harness.MinIOS3Config())
	require.NoError(t, err, "PUT /api/config with the S3 target should succeed")
	require.True(t, cfgResp.BackupConfigured,
		"pgBackRest must be configured (stanza-create succeeded); warning=%q hint=%q detail=%q",
		cfgResp.BackupWarning, cfgResp.BackupHint, cfgResp.BackupDetail)

	// Seed a known table with a fixed row count so each link of the chain has real,
	// deterministic content to back up. Done AFTER archiving is on so the WAL that
	// covers these writes is archived to the repo.
	require.NoError(t, env.PG.Exec(
		"CREATE TABLE marker(id int)"))
	require.NoError(t, env.PG.Exec(
		"INSERT INTO marker SELECT generate_series(1,1000)"))
	rows, err := env.PG.CountRows("marker")
	require.NoError(t, err)
	require.Equal(t, 1000, rows)

	// ---- Link 1: FULL ----
	fullRun, err := env.Panel.RunBackup("full")
	require.NoError(t, err, "POST /api/backups/run full should be accepted")
	require.Equal(t, "running", fullRun.Result)
	fullRec, err := env.Panel.AwaitBackup(fullRun.ID, 4*time.Minute)
	require.NoError(t, err)
	require.Equalf(t, "success", fullRec.Result, "full backup must succeed; error=%q", fullRec.Error)
	require.Equal(t, "full", fullRec.BackupType)

	countAfterFull, err := env.S3.CountObjects("backup/main")
	require.NoError(t, err)
	require.Greater(t, countAfterFull, 0, "the full backup must write objects under backup/main")

	// ---- Link 2: INCR (after 500 more rows) ----
	require.NoError(t, env.PG.Exec(
		"INSERT INTO marker SELECT generate_series(1001,1500)"))
	require.NoError(t, env.PG.Exec("SELECT pg_switch_wal()"))
	incrRun, err := env.Panel.RunBackup("incr")
	require.NoError(t, err, "POST /api/backups/run incr should be accepted")
	require.Equal(t, "running", incrRun.Result)
	incrRec, err := env.Panel.AwaitBackup(incrRun.ID, 4*time.Minute)
	require.NoError(t, err)
	require.Equalf(t, "success", incrRec.Result, "incr backup must succeed; error=%q", incrRec.Error)
	require.Equal(t, "incr", incrRec.BackupType)

	countAfterIncr, err := env.S3.CountObjects("backup/main")
	require.NoError(t, err)
	require.Greaterf(t, countAfterIncr, countAfterFull,
		"the incr backup must add objects under backup/main (full=%d incr=%d)", countAfterFull, countAfterIncr)

	// ---- Link 3: DIFF (after 200 more rows) ----
	require.NoError(t, env.PG.Exec(
		"INSERT INTO marker SELECT generate_series(1501,1700)"))
	require.NoError(t, env.PG.Exec("SELECT pg_switch_wal()"))
	diffRun, err := env.Panel.RunBackup("diff")
	require.NoError(t, err, "POST /api/backups/run diff should be accepted")
	require.Equal(t, "running", diffRun.Result)
	diffRec, err := env.Panel.AwaitBackup(diffRun.ID, 4*time.Minute)
	require.NoError(t, err)
	require.Equalf(t, "success", diffRec.Result, "diff backup must succeed; error=%q", diffRec.Error)
	require.Equal(t, "diff", diffRec.BackupType)

	countAfterDiff, err := env.S3.CountObjects("backup/main")
	require.NoError(t, err)
	require.Greaterf(t, countAfterDiff, countAfterIncr,
		"the diff backup must add objects under backup/main (incr=%d diff=%d)", countAfterIncr, countAfterDiff)

	// ---- Ground truth: the pgBackRest repo's own chain metadata ----
	infoJSON, err := env.ExecAsUser("postgres", "pgbackrest", "--stanza=main", "info", "--output=json")
	require.NoError(t, err, "pgbackrest info --output=json should succeed")

	var stanzas []pgbackrestStanza
	require.NoError(t, json.Unmarshal([]byte(infoJSON), &stanzas), "pgbackrest info must be valid JSON:\n%s", infoJSON)

	var main *pgbackrestStanza
	for i := range stanzas {
		if stanzas[i].Name == "main" {
			main = &stanzas[i]
			break
		}
	}
	require.NotNil(t, main, "stanza 'main' must be present in pgbackrest info:\n%s", infoJSON)

	// Bucket the repo's backups by type. The chain must be exactly one full, one
	// incr, one diff — no stray or expired entries.
	var fulls, incrs, diffs []pgbackrestBackup
	for _, b := range main.Backup {
		switch b.Type {
		case "full":
			fulls = append(fulls, b)
		case "incr":
			incrs = append(incrs, b)
		case "diff":
			diffs = append(diffs, b)
		}
	}
	require.Lenf(t, fulls, 1, "expected exactly one full in the repo chain, got %d: %+v", len(fulls), main.Backup)
	require.Lenf(t, incrs, 1, "expected exactly one incr in the repo chain, got %d: %+v", len(incrs), main.Backup)
	require.Lenf(t, diffs, 1, "expected exactly one diff in the repo chain, got %d: %+v", len(diffs), main.Backup)

	full := fulls[0]
	incr := incrs[0]
	diff := diffs[0]

	// The full is the chain root: it depends on nothing.
	require.Empty(t, full.Prior, "the full backup must have no prior (it is the chain root): %+v", full)
	require.Empty(t, full.Reference, "the full backup must reference no other backup: %+v", full)

	// The incr and diff each build on the full: their prior is the full's label and
	// their reference set includes the full's label.
	require.Equalf(t, full.Label, incr.Prior, "the incr's prior must be the full backup (incr=%+v)", incr)
	require.Truef(t, referencesLabel(incr.Reference, full.Label),
		"the incr must reference the full backup %q (incr.reference=%v)", full.Label, incr.Reference)

	require.Equalf(t, full.Label, diff.Prior, "the diff's prior must be the full backup (diff=%+v)", diff)
	require.Truef(t, referencesLabel(diff.Reference, full.Label),
		"the diff must reference the full backup %q (diff.reference=%v)", full.Label, diff.Reference)

	// Cross-check against the panel's own history: all three rows are present and
	// successful with the right types.
	hist, err := env.Panel.ListBackups()
	require.NoError(t, err)
	for _, want := range []struct {
		id  int64
		typ string
	}{
		{fullRun.ID, "full"},
		{incrRun.ID, "incr"},
		{diffRun.ID, "diff"},
	} {
		rec, ok := hist.FindBackup(want.id)
		require.Truef(t, ok, "backup id %d (%s) must appear in GET /api/backups", want.id, want.typ)
		require.Equalf(t, "success", rec.Result, "backup id %d (%s) must be successful", want.id, want.typ)
		require.Equalf(t, want.typ, rec.BackupType, "backup id %d must be of type %s", want.id, want.typ)
	}
}
