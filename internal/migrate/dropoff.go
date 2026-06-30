package migrate

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
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
	// DropCanceled is the terminal state for an operator-cancelled session. It is
	// DISTINCT from DropFailed: a failed session is retryable (its dump is kept in
	// S3 and ClaimDropoffForImport will re-claim it), but a cancelled session must
	// NEVER be re-startable — its presigned PUT URLs cannot be revoked once minted,
	// so a cancel that merely recorded 'failed' could be re-uploaded and restarted
	// via the API. 'canceled' is excluded from start/import claims yet still eligible
	// for expiry/object cleanup.
	DropCanceled DropStatus = "canceled"
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

// DropProbeKey returns the S3 key the mint-time reachability probe writes,
// stats, reads and deletes to confirm the panel can perform the FULL object
// lifecycle it will need (not just the source's PUT) before handing out a command:
// pg-migrations/dropoff/<code>/.probe. It is a disposable object, deleted by the
// probe itself; using a dedicated key keeps it distinct from the real dump/meta.
func DropProbeKey(code string) string {
	return DropPrefix + "/" + code + "/.probe"
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

// DropTable is one table's source row count in meta.json. Schema+Name MUST name
// the same set engine.RowCountsByTable enumerates (information_schema BASE TABLE
// outside pg_catalog/information_schema), or verification false-fails forever. The
// verification compares on the (schema, name) PAIR via RowCountsByTable, so a name
// containing a dot does not collide with a different pair (schema "a"/table "b.c"
// vs schema "a.b"/table "c").
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

// SourceRowCounts projects the meta tables into the flattened "schema.table" ->
// count map used only for the success RECORD (History/display); it shares
// engine.RowCounts's flattening and is NOT used for verification, which compares on
// the unambiguous (schema, name) pair via sourceRowCountsByTable.
func (m DropMeta) SourceRowCounts() map[string]int64 {
	out := make(map[string]int64, len(m.Tables))
	for _, t := range m.Tables {
		out[t.Schema+"."+t.Name] = t.Rows
	}
	return out
}

// sourceRowCountsByTable projects the meta tables into the unambiguous (schema,
// name) -> count map the drop-off verification compares against the target's
// engine.RowCountsByTable. parseDropMeta has already rejected any duplicate
// (schema, name) pair, so no claimed count is silently overwritten here.
func (m DropMeta) sourceRowCountsByTable() map[TableKey]int64 {
	out := make(map[TableKey]int64, len(m.Tables))
	for _, t := range m.Tables {
		out[TableKey{Schema: t.Schema, Name: t.Name}] = t.Rows
	}
	return out
}

// flattenRowCounts collapses a structured (schema, name) -> count map into the
// flattened "schema.name" -> count shape the success record/History store. It is
// only used after verification has matched every pair, so a (pathological) dot-name
// collision here cannot hide a mismatch — both colliding tables hold equal counts.
func flattenRowCounts(counts map[TableKey]int64) map[string]int64 {
	out := make(map[string]int64, len(counts))
	for k, n := range counts {
		out[k.Schema+"."+k.Name] = n
	}
	return out
}

// compareRowCountsByTable returns every table whose source and target counts differ,
// comparing on the unambiguous (schema, name) pair so a name that flattens to the
// same "schema.name" string as a different pair can never mask a real mismatch (the
// bug a flattened-key comparison has). The RowCountDiff.Table is the flattened name,
// used only for the human-readable mismatch summary. Sorted for stable output.
func compareRowCountsByTable(source, target map[TableKey]int64) []RowCountDiff {
	seen := make(map[TableKey]struct{}, len(source)+len(target))
	for k := range source {
		seen[k] = struct{}{}
	}
	for k := range target {
		seen[k] = struct{}{}
	}
	var diffs []RowCountDiff
	for k := range seen {
		s := source[k]
		t := target[k]
		if s != t {
			diffs = append(diffs, RowCountDiff{Table: k.Schema + "." + k.Name, Source: s, Target: t})
		}
	}
	sort.Slice(diffs, func(i, j int) bool { return diffs[i].Table < diffs[j].Table })
	return diffs
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
	// Reject a duplicate (schema, name) pair: the verification compares per pair, so a
	// repeated pair would mean one claimed count silently overwrites the other and a
	// real mismatch on the shadowed table could pass unnoticed. A conforming producer
	// lists each BASE TABLE exactly once.
	seen := make(map[TableKey]struct{}, len(m.Tables))
	for _, t := range m.Tables {
		k := TableKey{Schema: t.Schema, Name: t.Name}
		if _, dup := seen[k]; dup {
			return DropMeta{}, core.ValidationError(
				"drop-off metadata lists table %q in schema %q more than once", t.Name, t.Schema).
				WithHint("the metadata is malformed; re-run the push command on the source")
		}
		seen[k] = struct{}{}
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
	// Re-affirm the overwrite-safety gate immediately before preparing/restoring.
	// The first check ran during validation, BEFORE the (potentially multi-hour)
	// download — during which the target could have become non-empty. Without an
	// authorized overwrite we must refuse to write it now, exactly as the import
	// began: re-run the same gate so a target that filled up mid-download is never
	// silently clobbered.
	if err := o.validateTargetOverwrite(ctx, job); err != nil {
		return o.fail(ctx, rec, err)
	}
	createdTarget, err := o.prepareTarget(ctx, job)
	if err != nil {
		return o.fail(ctx, rec, err)
	}
	if err := o.engine.Restore(ctx, job.Target, dumpPath, job.TargetDatabase, RestoreOpts{
		Clean:   job.Overwrite,
		NoOwner: true,
	}); err != nil {
		if job.Overwrite {
			// The authoritative dump is in S3, so the local download is redundant for
			// retry — let cleanup() remove it rather than stranding up to MaxDropBytes
			// on disk until the next restart's sweep (each failed-overwrite retry would
			// otherwise leak another dump-sized file). preserveWorkDir is deliberately
			// NOT set here (unlike directSingle, whose local dump is the only copy).
			o.log.Error("drop-off restore failed after dropping original database; dump kept in S3 for retry",
				"database", job.TargetDatabase, "code", spec.Code)
			return o.fail(ctx, rec, core.ExecError("restore of %q failed after the original was dropped; the dump is kept in S3 for retry", job.TargetDatabase).Wrap(err))
		}
		// Non-overwrite path. Drop the target ONLY when THIS import created it: a
		// database we created holds nothing but this failed restore's partial output,
		// so dropping it lets a "Retry import" from the kept-in-S3 dump start clean —
		// otherwise the leftover tables read as non-empty and validateTargetOverwrite
		// would refuse every retry forever (the dump can't be re-pushed from the
		// hard-to-reach source).
		//
		// A target that PRE-EXISTED is never dropped. It passed validateTargetOverwrite
		// by holding no user BASE TABLEs, but that gate does NOT prove it is otherwise
		// empty: the operator may have created the database with extensions, custom
		// schemas, functions, sequences, views, or a non-default owner/encoding (e.g.
		// `CREATE DATABASE appdb; CREATE EXTENSION postgis;`). They explicitly declined
		// a destructive overwrite, so wiping their database on a transient restore
		// failure would violate the page's "never overwrites without typed
		// confirmation" invariant. Leave it untouched (matching directSingle) and tell
		// them how to retry. The dump stays in S3 either way.
		if createdTarget {
			o.dropCreatedTargetForCleanup(ctx, job, spec.Code, "restore failed")
			return o.fail(ctx, rec, err)
		}
		return o.fail(ctx, rec, core.ExecError(
			"restore of %q failed; the database existed before this import so it was left untouched — clear it (or enable Replace and re-type its name) before retrying from the kept-in-S3 dump", job.TargetDatabase).Wrap(err))
	}
	_ = rec.Progress(ctx, 1, 1, st.Size())

	// --- verifying -------------------------------------------------------
	// On a verification failure, drop the target ONLY when THIS import created it
	// (createdTarget): a database we created holds nothing but this restore's output,
	// so leaving it non-empty would make the NEXT non-overwrite retry fail its own
	// emptiness gate forever (the dump can't be re-pushed from the unreachable
	// source). This mirrors the restore-failure cleanup above. A PRE-EXISTING target
	// is never dropped — the operator declined a destructive overwrite.
	verifyFail := func(cause error) error {
		if createdTarget {
			o.dropCreatedTargetForCleanup(ctx, job, spec.Code, "verification failed")
		}
		return o.fail(ctx, rec, cause)
	}

	o.stage(ctx, rec, StatusImporting, PhaseVerifying)
	// Verify on the unambiguous (schema, name) pair, NOT the flattened "schema.name"
	// string: a name containing a dot (schema "a"/table "b.c" vs schema "a.b"/table
	// "c") would otherwise collapse two distinct tables onto one key and let a real
	// per-table mismatch be overwritten and pass. parseDropMeta has already rejected
	// duplicate pairs in the claimed counts.
	srcByTable := meta.sourceRowCountsByTable()
	tgtByTable, err := o.engine.RowCountsByTable(ctx, job.Target, job.TargetDatabase)
	if err != nil {
		return verifyFail(err)
	}
	if diffs := compareRowCountsByTable(srcByTable, tgtByTable); len(diffs) > 0 {
		return verifyFail(rowMismatchError(diffs))
	}
	// The verified counts are recorded in the flattened shape for History/display,
	// where the (harmless on a verified match) collision is acceptable.
	srcCounts := meta.SourceRowCounts()
	tgtCounts := flattenRowCounts(tgtByTable)

	// --- success: record completion FIRST, then reclaim the DB at rest ---
	// Persist success BEFORE deleting the S3 objects: a transient recorder error
	// after the objects were already gone would record a FAILURE with no artifact
	// left for the offered retry. With completion recorded first, a later delete
	// failure (transient S3 error, rotated creds) merely leaves the object at rest —
	// NOT orphaned, because the expiry sweep also reclaims completed sessions past
	// their TTL (ListExpiredDropoffs no longer excludes 'completed') as a backstop,
	// so the lingering dump is deleted and never outlives its TTL.
	if err := o.succeed(ctx, rec, srcCounts, tgtCounts); err != nil {
		return o.fail(ctx, rec, err)
	}
	if derr := tr.DeleteObject(ctx, spec.DumpKey); derr != nil {
		o.log.Warn("could not delete drop-off dump after import; the expiry sweep will reclaim it", "code", spec.Code, "error", derr)
	}
	if derr := tr.DeleteObject(ctx, spec.MetaKey); derr != nil {
		o.log.Warn("could not delete drop-off metadata after import; the expiry sweep will reclaim it", "code", spec.Code, "error", derr)
	}
	return nil
}

// dropCleanupTimeout bounds the detached DROP DATABASE cleanup below.
const dropCleanupTimeout = 30 * time.Second

// dropCreatedTargetForCleanup removes the target database THIS import created, as
// cleanup after a failed restore or verification. It runs on a context DETACHED from
// the worker's cancellation/deadline (context.WithoutCancel, then re-bounded): the
// failure that triggers cleanup is frequently the worker context EXPIRING on a slow
// or stalled transfer, and the bare (already-cancelled) ctx would make the DROP a
// guaranteed-failed no-op — leaving a non-empty database that blocks EVERY future
// non-overwrite retry (the dump cannot be re-pushed from the unreachable source).
// Best-effort + idempotent: a drop failure only logs and a retry can drop manually.
func (o *Orchestrator) dropCreatedTargetForCleanup(ctx context.Context, job Job, code, reason string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dropCleanupTimeout)
	defer cancel()
	if derr := o.engine.DropDatabase(ctx, job.Target, job.TargetDatabase); derr != nil {
		o.log.Warn("could not drop drop-off target created by this job; a retry may need a manual drop",
			"database", job.TargetDatabase, "code", code, "reason", reason, "error", derr)
	}
}
