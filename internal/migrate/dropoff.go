package migrate

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/venkatesh-sekar/indiepg/internal/core"
)

// ModeDropOff is the "drop-off link" migration mode: a single database is pushed
// from a source the panel CANNOT reach (NAT/firewall, no inbound, no panel, no
// AWS creds) but which CAN reach S3, via a presigned PUT URL the panel mints. The
// panel then imports the dump from S3 with the same SHA-256 + row-count
// verification as every other mode.
const ModeDropOff Mode = "drop-off"

// Drop-off S3 key layout and limits.
const (
	// DropPrefix is the S3 key prefix under which drop-off dumps and metadata live.
	// Like SessionPrefix it is time-boxed (the session TTL) and crypto-random per
	// code, the only safety the shared prefix has.
	DropPrefix = "pg-migrations/dropoff"

	// DropDefaultTTL is the lifetime of a drop-off session and of the presigned PUT
	// URLs it mints. Short by design: the URLs are bearer tokens and the dump is a
	// full database at rest.
	DropDefaultTTL = 2 * time.Hour

	// MaxDropBytes caps the dump size moved through a single presigned PUT. S3's
	// single-PUT object limit is 5 GiB; over that the operator must use direct-pull
	// (which streams and chunks). Enforced panel-side from the AUTHORITATIVE
	// StatObject size, never the source-supplied meta.byte_size.
	MaxDropBytes int64 = 5 << 30

	// MaxDropMetaBytes bounds the meta.json object the panel will read into memory.
	MaxDropMetaBytes int64 = 1 << 20

	// DropMetaSchemaVersion is the meta.json schema version migrate-push.sh writes.
	DropMetaSchemaVersion = 1
)

// DropStatus is the lifecycle state of a drop-off session, stored in the local
// SQLite dropoff_sessions table. It is distinct from the cross-panel session
// Status state machine.
type DropStatus string

const (
	// DropWaiting is the initial state: the URLs are minted, waiting for the source
	// to run the push script and upload the dump + meta.json.
	DropWaiting DropStatus = "waiting_for_upload"
	// DropUploaded means meta.json is present (the upload is complete and
	// verifiable); the panel may now Start the import.
	DropUploaded DropStatus = "uploaded"
	// DropImporting means the import worker is downloading/restoring/verifying.
	DropImporting DropStatus = "importing"
	// DropCompleted is the terminal success state (checksum + rows verified).
	DropCompleted DropStatus = "completed"
	// DropFailed is the terminal failure state.
	DropFailed DropStatus = "failed"
	// DropExpired is the terminal state for a session swept past its TTL.
	DropExpired DropStatus = "expired"
)

// DropDumpKey returns the S3 key of a drop-off session's dump object:
// pg-migrations/dropoff/<code>/dump.
func DropDumpKey(code string) string {
	return DropPrefix + "/" + code + "/dump"
}

// DropMetaKey returns the S3 key of a drop-off session's meta.json object:
// pg-migrations/dropoff/<code>/meta.json. Its presence == upload complete.
func DropMetaKey(code string) string {
	return DropPrefix + "/" + code + "/meta.json"
}

// DropTransport is the S3 capability surface the drop-off mode needs. It is a
// superset of the 3-method ObjectStore (kept intact for the ssh-less fakes):
// minting the presigned PUT (PresignPut), checking readiness/size authoritatively
// (StatObject), streaming the dump to disk under a hard byte ceiling
// (DownloadToFile), reading the small meta.json with a hard ceiling
// (GetObjectLimited), and cleaning up after import (DeleteObject).
// *backup.S3ObjectStore satisfies it.
//
// BOTH object reads are size-bounded, not merely pre-checked via StatObject: the
// dump-key and meta-key presigned PUTs are PUT-only bearer tokens valid for the
// full TTL and can be re-uploaded, so a holder of either URL could swap in a much
// larger object between the StatObject pre-check and the read (a TOCTOU). The
// bounded read — GetObjectLimited for meta, the max argument to DownloadToFile for
// the dump — is therefore the authoritative guard, keeping the single binary from
// OOMing on a swapped-in giant manifest or exhausting the disk on a swapped-in
// giant dump.
type DropTransport interface {
	PresignPut(ctx context.Context, key string, ttl time.Duration) (string, error)
	StatObject(ctx context.Context, key string) (size int64, exists bool, err error)
	DownloadToFile(ctx context.Context, key, dest string, max int64) error
	GetObjectLimited(ctx context.Context, key string, max int64) ([]byte, error)
	DeleteObject(ctx context.Context, key string) error
}

// DropTable is one table's source row count in meta.json. Schema+Name MUST be
// the same set engine.RowCounts enumerates (information_schema BASE TABLE outside
// pg_catalog/information_schema), keyed schema.name, or CompareRowCounts
// false-fails forever.
type DropTable struct {
	Schema string `json:"schema"`
	Name   string `json:"name"`
	Rows   int64  `json:"rows"`
}

// DropMeta is the meta.json document migrate-push.sh uploads AFTER the dump. Its
// presence is the panel's "upload complete & verifiable" signal.
type DropMeta struct {
	SchemaVersion int         `json:"schema_version"`
	SourceDB      string      `json:"source_db"`
	PGVersion     string      `json:"pg_version"`
	SHA256        string      `json:"sha256"`
	ByteSize      int64       `json:"byte_size"`
	CreatedAt     string      `json:"created_at"`
	Tables        []DropTable `json:"tables"`
	TotalRows     int64       `json:"total_rows"`
}

// SourceRowCounts projects the meta tables into the "schema.table" -> count map
// shape CompareRowCounts and engine.RowCounts use. Keyed EXACTLY schema+"."+name
// to match engine.RowCounts (engine.go).
func (m DropMeta) SourceRowCounts() map[string]int64 {
	out := make(map[string]int64, len(m.Tables))
	for _, t := range m.Tables {
		out[t.Schema+"."+t.Name] = t.Rows
	}
	return out
}

// parseDropMeta unmarshals and minimally validates a meta.json document.
func parseDropMeta(data []byte) (DropMeta, error) {
	var m DropMeta
	if err := json.Unmarshal(data, &m); err != nil {
		return DropMeta{}, core.ValidationError("malformed drop-off metadata").Wrap(err)
	}
	// Gate compatibility on the schema version so a future push script that bumps
	// the manifest format gets a clear "update indiepg" error instead of being
	// silently mis-parsed. Version 0 means the field was absent (a pre-v1 or
	// non-conforming producer).
	if m.SchemaVersion <= 0 || m.SchemaVersion > DropMetaSchemaVersion {
		return DropMeta{}, core.ValidationError(
			"drop-off metadata schema version %d is not supported (this panel understands up to %d)",
			m.SchemaVersion, DropMetaSchemaVersion).
			WithHint("re-copy the push command from the panel, or update indiepg on the panel")
	}
	if m.SHA256 == "" {
		return DropMeta{}, core.ValidationError("drop-off metadata is missing a checksum").
			WithHint("re-run the push command on the source")
	}
	return m, nil
}

// DropImportSpec is the input to ImportFromDrop: the S3 keys to pull, the local
// target to restore into, and the overwrite decision already authorized (with a
// typed-name confirmation) at mint time.
type DropImportSpec struct {
	Code           string
	DumpKey        string
	MetaKey        string
	TargetDatabase string
	Overwrite      bool
	Target         ConnInfo
}

// errDropTooLarge is the friendly refusal when the uploaded dump exceeds the
// single-PUT ceiling, pointing the operator at direct-pull (which streams).
func errDropTooLarge(code string, size int64) error {
	return core.ValidationError(
		"drop-off %s dump is %d MiB, over the %d MiB single-PUT limit — use the direct-pull migration instead",
		code, size>>20, MaxDropBytes>>20).
		WithHint("direct-pull streams the dump and has no size limit")
}

// ImportFromDrop is the TARGET side of a drop-off migration. It is a NEW
// streaming method (NOT a fork of ImportFromSession, which buffers the whole dump
// in memory and is welded to session.json): it confirms the upload via the
// AUTHORITATIVE StatObject, streams the dump to disk with DownloadToFile (no
// in-memory ceiling), verifies the SHA-256 against meta.json, restores into the
// local target reusing the overwrite/typed-confirm gate, compares restored
// per-table row counts to the source counts in meta.json (CompareRowCounts), and
// only on a full match deletes the S3 objects and records success. On any failure
// the S3 objects are KEPT so the operator can retry/cancel.
//
// tr is passed as an argument (NOT stored on the Orchestrator) so NewOrchestrator's
// signature — depended on by the existing workers — is unchanged.
func (o *Orchestrator) ImportFromDrop(ctx context.Context, tr DropTransport, spec DropImportSpec, rec Recorder) error {
	defer o.cleanup()
	if tr == nil {
		return o.fail(ctx, rec, core.ValidationError("drop-off import requires S3 object storage"))
	}

	// --- validating ------------------------------------------------------
	o.stage(ctx, rec, StatusImporting, PhaseValidating)

	// meta.json present == the source finished uploading (it is written LAST).
	metaSize, metaExists, err := tr.StatObject(ctx, spec.MetaKey)
	if err != nil {
		return o.fail(ctx, rec, core.InternalError("cannot check drop-off upload status for %s", spec.Code).Wrap(err))
	}
	if !metaExists {
		return o.fail(ctx, rec, core.ConflictError("drop-off %s is not uploaded yet", spec.Code).
			WithHint("run the push command on the source, then click Start"))
	}
	if metaSize > MaxDropMetaBytes {
		return o.fail(ctx, rec, core.ValidationError("drop-off %s metadata is implausibly large (%d bytes)", spec.Code, metaSize))
	}

	// Authoritative dump size — panel-side, never trusting meta.byte_size, which a
	// presigned PUT cannot enforce.
	dumpSize, dumpExists, err := tr.StatObject(ctx, spec.DumpKey)
	if err != nil {
		return o.fail(ctx, rec, core.InternalError("cannot check drop-off dump for %s", spec.Code).Wrap(err))
	}
	if !dumpExists {
		return o.fail(ctx, rec, core.ConflictError("drop-off %s dump object is missing", spec.Code).
			WithHint("the upload did not complete; re-run the push command"))
	}
	if dumpSize > MaxDropBytes {
		return o.fail(ctx, rec, errDropTooLarge(spec.Code, dumpSize))
	}

	// Overwrite-safety gate (identical to direct single-db): refuse a non-empty
	// target unless overwrite was authorized.
	job := Job{Target: spec.Target, TargetDatabase: spec.TargetDatabase, Overwrite: spec.Overwrite}
	if err := o.validateTargetOverwrite(ctx, job); err != nil {
		return o.fail(ctx, rec, err)
	}

	// Read + parse meta.json. The read is HARD-bounded (not merely pre-checked via
	// StatObject above, which is a TOCTOU): a holder of the meta-key presigned PUT
	// could swap in a huge object between the stat and this read, so the bounded
	// read — not the stat — is the authoritative memory guard.
	metaRaw, err := tr.GetObjectLimited(ctx, spec.MetaKey, MaxDropMetaBytes)
	if err != nil {
		return o.fail(ctx, rec, core.InternalError("cannot read drop-off metadata for %s", spec.Code).Wrap(err))
	}
	meta, err := parseDropMeta(metaRaw)
	if err != nil {
		return o.fail(ctx, rec, err)
	}
	// The source's claimed size must match the authoritative object size, or the
	// upload is incomplete / the metadata is stale.
	if meta.ByteSize != dumpSize {
		return o.fail(ctx, rec, core.ValidationError(
			"drop-off %s dump size mismatch (metadata says %d, object is %d bytes)", spec.Code, meta.ByteSize, dumpSize).
			WithHint("the upload is incomplete or the metadata is stale; re-run the push command"))
	}

	// --- downloading (streamed to disk; bounded by the single-PUT ceiling) ---
	// The download is hard-capped at MaxDropBytes, not merely pre-checked by the
	// StatObject above (a TOCTOU): a holder of the dump-key presigned PUT could swap
	// a much larger object in after the stat, and DownloadToFile would otherwise
	// write it whole and exhaust the disk before the SHA-256 mismatch ever rejected
	// it. The bounded transfer makes the dump guard authoritative the way
	// GetObjectLimited already makes the meta guard authoritative.
	o.stage(ctx, rec, StatusImporting, PhaseDownloading)
	dumpPath := filepath.Join(o.workDir, "dropoff.dump")
	if err := tr.DownloadToFile(ctx, spec.DumpKey, dumpPath, MaxDropBytes); err != nil {
		return o.fail(ctx, rec, core.InternalError("failed to download drop-off dump for %s", spec.Code).Wrap(err))
	}
	st, err := os.Stat(dumpPath)
	if err != nil {
		return o.fail(ctx, rec, core.InternalError("downloaded drop-off dump is missing").Wrap(err))
	}
	if st.Size() == 0 {
		return o.fail(ctx, rec, core.ValidationError("drop-off %s dump is empty", spec.Code).
			WithHint("the source produced an empty dump; check the database name"))
	}
	_ = rec.Progress(ctx, 0, 1, st.Size())

	// --- integrity: SHA-256 must match meta BEFORE any restore ----------
	sum, err := fileSHA256(dumpPath)
	if err != nil {
		return o.fail(ctx, rec, err)
	}
	if sum != meta.SHA256 {
		return o.fail(ctx, rec, core.ValidationError(
			"drop-off %s dump checksum mismatch (got %s, expected %s)", spec.Code, sum, meta.SHA256).
			WithHint("the uploaded dump is corrupt or incomplete; re-run the push command"))
	}

	// --- restoring -------------------------------------------------------
	o.stage(ctx, rec, StatusImporting, PhaseRestoring)
	if err := o.prepareTarget(ctx, job); err != nil {
		return o.fail(ctx, rec, err)
	}
	if err := o.engine.Restore(ctx, job.Target, dumpPath, job.TargetDatabase, RestoreOpts{
		Clean:   job.Overwrite,
		NoOwner: true,
	}); err != nil {
		if job.Overwrite {
			o.log.Error("drop-off restore failed after dropping original database; dump kept in S3 for retry",
				"database", job.TargetDatabase, "code", spec.Code)
			o.preserveWorkDir = true
			return o.fail(ctx, rec, core.ExecError("restore of %q failed after the original was dropped; the dump is kept in S3 for retry", job.TargetDatabase).Wrap(err))
		}
		// Non-overwrite path: validateTargetOverwrite guaranteed the target was
		// EMPTY (or absent) before this attempt, so any tables now present can only
		// be this failed restore's partial output. Drop it so a "Retry import" from
		// the kept-in-S3 dump starts clean — otherwise the leftover tables read as
		// non-empty and validateTargetOverwrite would refuse every retry forever
		// (the dump can't be re-pushed from the hard-to-reach source). The dump is
		// kept in S3; only the poisoned local target is reset.
		if derr := o.engine.DropDatabase(ctx, job.Target, job.TargetDatabase); derr != nil {
			o.log.Warn("could not drop partially-restored drop-off target after a failed restore; a retry may need a manual drop",
				"database", job.TargetDatabase, "code", spec.Code, "error", derr)
		}
		return o.fail(ctx, rec, err)
	}
	_ = rec.Progress(ctx, 1, 1, st.Size())

	// --- verifying -------------------------------------------------------
	o.stage(ctx, rec, StatusImporting, PhaseVerifying)
	srcCounts := meta.SourceRowCounts()
	tgtCounts, err := o.engine.RowCounts(ctx, job.Target, job.TargetDatabase)
	if err != nil {
		return o.fail(ctx, rec, err)
	}
	if diffs := CompareRowCounts(srcCounts, tgtCounts); len(diffs) > 0 {
		return o.fail(ctx, rec, rowMismatchError(diffs))
	}

	// --- success: remove the full DB at rest, then record completion ----
	// Best-effort: if a delete fails here (transient S3 error, rotated creds) the
	// object is NOT orphaned — the expiry sweep also reclaims completed sessions
	// past their TTL (ListExpiredDropoffs no longer excludes 'completed'), so the
	// lingering dump is deleted as a backstop and never outlives its TTL.
	if derr := tr.DeleteObject(ctx, spec.DumpKey); derr != nil {
		o.log.Warn("could not delete drop-off dump after import; the expiry sweep will reclaim it", "code", spec.Code, "error", derr)
	}
	if derr := tr.DeleteObject(ctx, spec.MetaKey); derr != nil {
		o.log.Warn("could not delete drop-off metadata after import; the expiry sweep will reclaim it", "code", spec.Code, "error", derr)
	}
	if err := o.succeed(ctx, rec, srcCounts, tgtCounts); err != nil {
		return o.fail(ctx, rec, err)
	}
	return nil
}
