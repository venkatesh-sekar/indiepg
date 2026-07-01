//go:build e2e

package e2e

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/indiepg/test/e2e/harness"
)

// markerObjectName is the single-writer ownership marker file the panel writes
// at the root of its backup repo prefix (internal/identity.MarkerObjectName).
const markerObjectName = ".panel-owner.json"

// ownershipMarker mirrors the on-disk JSON the panel writes/reads
// (internal/identity.OwnershipMarker). We decode the panel's real marker and
// encode a forged foreign one with the same shape.
type ownershipMarker struct {
	InstanceID string    `json:"instance_id"`
	Hostname   string    `json:"hostname"`
	PGSystemID string    `json:"pg_system_id"`
	ClaimedAt  time.Time `json:"claimed_at"`
	LastSeen   time.Time `json:"last_seen"`
}

// TestOwnershipFailClosed is scenario 17: single-writer ownership fail-closed.
//
// Two panels must never share one pgBackRest repository, or both will corrupt
// it. The panel enforces this with a .panel-owner.json marker carrying its own
// instance id; before every write it claims/verifies the marker and HARD STOPS
// on a foreign owner.
//
// This test proves the guard end-to-end against real S3 (MinIO):
//  1. Configure S3 and take a full backup so the panel establishes its marker.
//  2. Confirm the marker is real and ours (its instance_id == the panel's).
//  3. DIRECTLY overwrite the marker in the bucket with a FOREIGN owner id,
//     bypassing the panel entirely (as a second, hostile panel would appear).
//  4. Attempt another backup → it must FAIL FAST with an ownership error and
//     must NOT clobber the foreign marker (it refuses to touch the repo).
func TestOwnershipFailClosed(t *testing.T) {
	t.Parallel()

	env := harness.Up(t, harness.Options{Image: harness.ImagePreinstalled})
	require.NoError(t, env.Panel.Login(harness.AdminPassword))

	// Configure the S3 target; this initializes the repo (stanza-create), and
	// constructs the panel's single-writer Owner over the same bucket.
	cfgResp, err := env.Panel.ConfigureS3(harness.MinIOS3Config())
	require.NoError(t, err, "PUT /api/config with the S3 target should succeed")
	require.True(t, cfgResp.BackupConfigured,
		"pgBackRest must be configured; warning=%q hint=%q detail=%q",
		cfgResp.BackupWarning, cfgResp.BackupHint, cfgResp.BackupDetail)

	// Seed a little content so the full backup has something to write, then force
	// a WAL switch so the backup completes against archived WAL (mirrors the
	// proven backup-full flow).
	require.NoError(t, env.PG.Exec(
		"CREATE TABLE IF NOT EXISTS e2e_owner_probe(id int PRIMARY KEY)"))
	require.NoError(t, env.PG.Exec(
		"INSERT INTO e2e_owner_probe SELECT g FROM generate_series(1,10) g ON CONFLICT DO NOTHING"))
	require.NoError(t, env.PG.Exec("SELECT pg_switch_wal()"))

	// (1) Take a full backup. Its synchronous ownership Claim writes the marker.
	run, err := env.Panel.RunBackup("full")
	require.NoError(t, err, "POST /api/backups/run full should be accepted")
	require.Equal(t, "running", run.Result)

	rec, err := env.Panel.AwaitBackup(run.ID, 5*time.Minute)
	require.NoError(t, err)
	require.Equalf(t, "success", rec.Result, "the establishing backup must succeed; error=%q", rec.Error)

	// (2) The marker is real and belongs to THIS panel.
	markerKey, err := env.S3.FindObjectWithSuffix("", markerObjectName)
	require.NoError(t, err)
	require.NotEmpty(t, markerKey,
		"the panel must have written its %s ownership marker after a backup", markerObjectName)

	rawMine, err := env.S3.GetObject(markerKey)
	require.NoError(t, err)
	var mine ownershipMarker
	require.NoError(t, json.Unmarshal(rawMine, &mine), "the marker must be valid JSON")

	inst, err := env.Panel.Instance()
	require.NoError(t, err)
	require.NotEmpty(t, inst.InstanceID)
	require.Equal(t, inst.InstanceID, mine.InstanceID,
		"the established marker must be owned by this panel's instance id")

	// (3) Plant a FOREIGN, ACTIVE owner directly in the bucket — exactly what a
	// second panel pointed at the same repo would leave behind. A recent LastSeen
	// makes it non-stale, so it is a non-adoptable HARD STOP (not a recoverable
	// "abandoned repo").
	const foreignID = "ffffffff-ffff-4fff-8fff-ffffffffffff"
	now := time.Now().UTC()
	foreign := ownershipMarker{
		InstanceID: foreignID,
		Hostname:   "intruder.example",
		PGSystemID: "1234567890123456789",
		ClaimedAt:  now.Add(-1 * time.Hour),
		LastSeen:   now,
	}
	foreignJSON, err := json.Marshal(foreign)
	require.NoError(t, err)
	require.NoError(t, env.S3.PutObject(markerKey, foreignJSON),
		"planting the foreign ownership marker should succeed")

	// (4) Another backup must FAIL FAST: the synchronous Claim reads the foreign
	// marker and HARD STOPS before any pgBackRest write. The error surfaces inline
	// on the POST (no "running" row is created) as a typed ownership conflict.
	_, err = env.Panel.RunBackup("full")
	require.Error(t, err, "a backup against a foreign-owned repo must be refused")

	var pe *harness.PanelError
	require.True(t, errors.As(err, &pe), "expected a typed panel error, got %T: %v", err, err)
	require.Equal(t, "ownership", pe.Code,
		"the refusal must be a single-writer ownership conflict")
	require.Equal(t, 409, pe.Status, "an ownership conflict is an HTTP 409")
	require.Contains(t, pe.Message, foreignID,
		"the error must name the foreign owner that holds the repo; message=%q", pe.Message)
	require.Contains(t, strings.ToLower(pe.Message), "owned by panel",
		"the error must explain the repo is owned by another panel; message=%q", pe.Message)

	// And the panel must NOT have clobbered the foreign marker — it refuses to
	// write to a repo it does not own, marker included.
	rawAfter, err := env.S3.GetObject(markerKey)
	require.NoError(t, err)
	var after ownershipMarker
	require.NoError(t, json.Unmarshal(rawAfter, &after))
	require.Equal(t, foreignID, after.InstanceID,
		"the panel must not overwrite a foreign owner's marker")
}
