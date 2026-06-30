package backup

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/identity"
)

// Presigned-URL expiry bounds. minio's signer rejects an expiry under 1 second
// or over 7 days (api-presigned.go -> isValidExpiry), so PresignPut clamps the
// caller's ttl into this range before signing.
const (
	minPresignTTL = 1 * time.Second
	maxPresignTTL = 7 * 24 * time.Hour
)

// S3ObjectStore adapts minio-go to identity.ObjectStore, the minimal S3 surface
// the single-writer ownership marker needs (read/write/conditional-create/delete
// of small JSON objects). It is the concrete implementation the design refers to
// ("the real implementation in internal/backup adapts minio-go"): the marker
// must live in the SAME bucket/prefix pgBackRest writes to, so a second panel
// pointed at the repo can see it and refuse to share.
type S3ObjectStore struct {
	client *minio.Client
	bucket string

	// versioned caches the bucket's S3 versioning state, but ONLY a positive result
	// (nil means "not known to be versioned"). On a VERSIONED (or version-suspended)
	// bucket a plain RemoveObject only writes a delete marker and leaves every prior
	// version — including a full database dump — stored indefinitely, so DeleteObject
	// must purge ALL versions of a key instead. We never turn versioning ON or OFF, so
	// once observed Enabled/Suspended a bucket stays that way for us and the positive
	// answer is cached behind verMu to spare a round trip per delete on the sweep path.
	// A NEGATIVE answer is deliberately NOT cached: versioning can be enabled on a
	// bucket at any time, so a cached "false" could later become unsafe; we re-check.
	verMu     sync.Mutex
	versioned *bool
}

// compile-time assertion that the adapter satisfies the marker's store surface.
var _ identity.ObjectStore = (*S3ObjectStore)(nil)

// S3StoreParams configures an S3ObjectStore from the panel's S3 target. The
// fields mirror config.S3Target; the secret is passed verbatim (never trimmed).
type S3StoreParams struct {
	Endpoint  string
	Region    string
	Bucket    string
	AccessKey string
	SecretKey string
	UseSSL    bool
	// PathStyle selects path-style addressing (MinIO); the default is host/
	// virtual-hosted style (AWS S3, Backblaze B2, Cloudflare R2), matching the
	// pgBackRest repo1-s3-uri-style default.
	PathStyle bool
}

// NewS3ObjectStore builds an S3ObjectStore from params. It requires an endpoint
// and bucket (the marker has nowhere else to live) and tolerates an endpoint
// pasted with a scheme or trailing slash.
func NewS3ObjectStore(p S3StoreParams) (*S3ObjectStore, error) {
	endpoint := strings.TrimSpace(p.Endpoint)
	// minio.New wants host[:port] with NO scheme; strip a pasted https:// prefix
	// and any trailing slash so a copy-pasted console URL still works.
	endpoint = strings.TrimPrefix(endpoint, "https://")
	endpoint = strings.TrimPrefix(endpoint, "http://")
	endpoint = strings.TrimSuffix(endpoint, "/")
	bucket := strings.TrimSpace(p.Bucket)
	if endpoint == "" {
		return nil, core.ValidationError("backup: S3 endpoint is required for ownership-marker storage")
	}
	if bucket == "" {
		return nil, core.ValidationError("backup: S3 bucket is required for ownership-marker storage")
	}

	lookup := minio.BucketLookupDNS
	if p.PathStyle {
		lookup = minio.BucketLookupPath
	}

	client, err := minio.New(endpoint, &minio.Options{
		Creds:        credentials.NewStaticV4(strings.TrimSpace(p.AccessKey), p.SecretKey, ""),
		Secure:       p.UseSSL,
		Region:       strings.TrimSpace(p.Region),
		BucketLookup: lookup,
	})
	if err != nil {
		return nil, core.InternalError("backup: construct S3 client").Wrap(err)
	}
	return &S3ObjectStore{client: client, bucket: bucket}, nil
}

// GetObject returns the object bytes, or a *core.Error with CodeNotFound when
// the key is absent (the contract identity.Owner relies on to tell "unclaimed"
// from a transport error).
func (s *S3ObjectStore) GetObject(ctx context.Context, key string) ([]byte, error) {
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, s.classifyGet(err, key)
	}
	defer func() { _ = obj.Close() }()
	// minio's GetObject is lazy: the request is issued on first read, so a missing
	// key surfaces here rather than above.
	data, err := io.ReadAll(obj)
	if err != nil {
		return nil, s.classifyGet(err, key)
	}
	return data, nil
}

// GetObjectLimited returns the object bytes but refuses to buffer more than max
// bytes into memory: it reads at most max+1 via an io.LimitReader and rejects the
// object as too large when that ceiling is reached. It is the safe read for an
// object whose size is attacker-influenceable — notably a drop-off meta.json,
// whose presigned PUT URL can be re-uploaded with arbitrary content within its
// TTL — so the single binary cannot be OOM'd by a swapped-in giant manifest. A
// missing key maps to CodeNotFound per the GetObject contract.
func (s *S3ObjectStore) GetObjectLimited(ctx context.Context, key string, max int64) ([]byte, error) {
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, s.classifyGet(err, key)
	}
	defer func() { _ = obj.Close() }()
	// minio's GetObject is lazy: the request is issued on first read, so a missing
	// key surfaces here. Read one byte past the ceiling so an over-limit object is
	// detectable without buffering it.
	data, err := io.ReadAll(io.LimitReader(obj, max+1))
	if err != nil {
		return nil, s.classifyGet(err, key)
	}
	if int64(len(data)) > max {
		return nil, core.ValidationError("backup: S3 object %q exceeds the %d-byte read limit", key, max)
	}
	return data, nil
}

// PutObject writes (or overwrites) the object.
func (s *S3ObjectStore) PutObject(ctx context.Context, key string, data []byte) error {
	_, err := s.client.PutObject(ctx, s.bucket, key, bytes.NewReader(data), int64(len(data)),
		minio.PutObjectOptions{ContentType: "application/json"})
	if err != nil {
		return core.InternalError("backup: write S3 object %q", key).Wrap(err)
	}
	return nil
}

// PutObjectIfAbsent atomically creates the object only when absent, using an
// If-None-Match:* precondition. On a losing race (the object already exists) it
// returns identity.ErrPreconditionFailed (matchable with errors.Is), which the
// Owner treats as "another writer claimed it first". This atomicity is what
// closes the claim TOCTOU race; If-None-Match:* is honored by AWS S3, R2, MinIO
// and Backblaze B2.
func (s *S3ObjectStore) PutObjectIfAbsent(ctx context.Context, key string, data []byte) error {
	opts := minio.PutObjectOptions{ContentType: "application/json"}
	opts.SetMatchETagExcept("*") // -> If-None-Match: *
	_, err := s.client.PutObject(ctx, s.bucket, key, bytes.NewReader(data), int64(len(data)), opts)
	if err == nil {
		return nil
	}
	if minio.ToErrorResponse(err).StatusCode == http.StatusPreconditionFailed {
		return identity.ErrPreconditionFailed
	}
	return core.InternalError("backup: conditional-create S3 object %q", key).Wrap(err)
}

// DeleteObject DURABLY removes the object. A missing object is not an error.
//
// On a non-versioned bucket this is a plain RemoveObject. On a VERSIONED (or
// version-suspended) bucket a plain RemoveObject only writes a delete marker and
// leaves the object's data versions — a full database dump, in the drop-off case —
// at rest forever; the sweep would then record the session 'expired' as if the data
// were reclaimed. So when the bucket retains versions, every version of the key is
// purged instead (streamed list + RemoveObject per VersionID). If a version cannot
// be removed (Object Lock / retention), that surfaces as an error so the caller does
// not falsely believe the data is gone — and the mint-time probe (ProbePutReachable)
// catches such a bucket up front.
//
// FAIL CLOSED: if the bucket's versioning state cannot even be READ (e.g. a minimal
// policy without s3:GetBucketVersioning), we do NOT fall back to a plain delete — a
// plain delete on a bucket that is in fact versioned would silently leave the dump's
// data versions behind. We surface the lookup error instead, so the mint-time probe
// rejects such a bucket up front and a cleanup/sweep retries rather than recording an
// un-erased dump as reclaimed.
func (s *S3ObjectStore) DeleteObject(ctx context.Context, key string) error {
	versioned, verr := s.bucketVersioningEnabled(ctx)
	if verr != nil {
		return verr // cannot prove a plain delete is durable; fail closed
	}
	if versioned {
		return s.purgeAllVersions(ctx, key)
	}
	if err := s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return core.InternalError("backup: delete S3 object %q", key).Wrap(err)
	}
	return nil
}

// bucketVersioningEnabled reports whether the bucket retains object versions (S3
// versioning Enabled OR Suspended), caching only a POSITIVE result (see the
// S3ObjectStore.versioned doc). A read failure is returned to the caller, which
// FAILS CLOSED rather than assuming the safe-looking non-versioned case.
func (s *S3ObjectStore) bucketVersioningEnabled(ctx context.Context) (bool, error) {
	s.verMu.Lock()
	defer s.verMu.Unlock()
	if s.versioned != nil {
		return *s.versioned, nil // only ever cached when true
	}
	vc, err := s.client.GetBucketVersioning(ctx, s.bucket)
	if err != nil {
		return false, core.InternalError("backup: read S3 bucket versioning for %q", s.bucket).Wrap(err)
	}
	// BOTH Enabled and Suspended retain prior versions: suspending versioning stops
	// NEW versions but does NOT delete the ones already at rest, so a plain delete on a
	// suspended-but-previously-versioned bucket would still leave the dump behind.
	// Treat suspended as versioned and purge by VersionID.
	v := vc.Enabled() || vc.Suspended()
	if v {
		// Cache ONLY the positive answer: a "false" must be re-checked because
		// versioning could be enabled on the bucket later.
		cached := true
		s.versioned = &cached
	}
	return v, nil
}

// purgeAllVersions removes EVERY version (and delete marker) of key on a versioned
// bucket, so the object's data is truly erased rather than merely hidden behind a
// delete marker. It STREAMS the version listing and removes each matching version by
// VersionID as it is listed, NEVER accumulating every version in memory: a holder of
// the still-valid presigned PUT could create an unbounded number of versions, and
// buffering them all would exhaust the panel. Any removal failure (e.g. an
// Object-Lock-retained version) is returned so the caller knows the data is NOT gone.
func (s *S3ObjectStore) purgeAllVersions(ctx context.Context, key string) error {
	// ListObjectsIter is the range-over-func form: breaking/returning out of the loop
	// stops it cleanly and defer cancel() aborts any in-flight request, with NO
	// background producer goroutine to leak. The channel-based ListObjects requires the
	// caller to fully DRAIN the channel after cancelling (minio buffers it size-1 and
	// blocks the producer on the terminal ctx-error send otherwise) — easy to get wrong
	// on the early-return-on-delete-failure path below, so we use the iterator instead.
	listCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	for info := range s.client.ListObjectsIter(listCtx, s.bucket, minio.ListObjectsOptions{
		Prefix:       key,
		WithVersions: true,
		Recursive:    true,
	}) {
		if info.Err != nil {
			return core.InternalError("backup: list versions of S3 object %q", key).Wrap(info.Err)
		}
		// A version listing taken with a Prefix also yields keys that merely START with
		// key (e.g. "dump" matches "dump2"); only the EXACT key's versions are ours.
		// Delete markers ARE removable versions and must be purged too so no trace is left.
		if info.Key != key {
			continue
		}
		// Remove THIS version immediately rather than collecting every VersionID first,
		// keeping memory bounded regardless of how many versions exist. GovernanceBypass
		// lets the panel reclaim its own dump under GOVERNANCE-mode Object Lock (the only
		// bypassable mode); COMPLIANCE-mode retention is not bypassable and surfaces here
		// as an error, failing the cleanup honestly.
		if err := s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{
			VersionID:        info.VersionID,
			GovernanceBypass: true,
		}); err != nil {
			return core.InternalError("backup: delete version of S3 object %q", key).Wrap(err)
		}
	}
	return nil
}

// countDataVersions returns how many NON-delete-marker versions of key remain at
// rest. It is the probe's durability check on a versioned bucket: after a delete,
// zero means the data is truly gone; non-zero means versioning + Object Lock /
// retention is keeping a dump alive that the panel could never reclaim.
func (s *S3ObjectStore) countDataVersions(ctx context.Context, key string) (int, error) {
	// Iterator form (see purgeAllVersions): an early return on a list error stops it
	// cleanly without leaking a producer goroutine blocked on the channel's terminal send.
	listCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	n := 0
	for info := range s.client.ListObjectsIter(listCtx, s.bucket, minio.ListObjectsOptions{
		Prefix:       key,
		WithVersions: true,
		Recursive:    true,
	}) {
		if info.Err != nil {
			return 0, info.Err
		}
		if info.Key != key || info.IsDeleteMarker {
			continue
		}
		n++
	}
	return n, nil
}

// PresignPut returns a short-lived, single-key, PUT-only presigned URL for key.
// It is the transport for the drop-off migration mode: the panel mints the URL
// and hands it to a source box that cannot otherwise reach S3 with credentials.
// The minio V4 signature binds the URL to exactly PUT + this one object key, so
// it is a bounded bearer token — safe to paste into the operator's own shell but
// still a secret, so the returned URL is NEVER logged or persisted in plaintext.
//
// ttl is clamped to minio's accepted 1s..7d range; callers should pass the
// session TTL (e.g. migrate.DropDefaultTTL).
func (s *S3ObjectStore) PresignPut(ctx context.Context, key string, ttl time.Duration) (string, error) {
	if ttl < minPresignTTL {
		ttl = minPresignTTL
	}
	if ttl > maxPresignTTL {
		ttl = maxPresignTTL
	}
	// No Content-Type / extra signed headers: keep the canonical request minimal so
	// a plain `curl --upload-file` (which adds only unsigned headers) satisfies the
	// signature on AWS S3, R2, B2 and MinIO alike.
	u, err := s.client.PresignedPutObject(ctx, s.bucket, key, ttl)
	if err != nil {
		// Deliberately do not include the (would-be) URL in the error.
		return "", core.InternalError("backup: presign PUT for S3 object %q", key).Wrap(err)
	}
	return u.String(), nil
}

// StatObject reports an object's size and whether it exists. A missing object is
// the normal "not uploaded yet" signal, returned as (0, false, nil) rather than
// an error, so the drop-off readiness check can branch on it cleanly. Any other
// transport failure is returned as an internal error.
func (s *S3ObjectStore) StatObject(ctx context.Context, key string) (int64, bool, error) {
	info, err := s.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		if core.CodeOf(s.classifyGet(err, key)) == core.CodeNotFound {
			return 0, false, nil
		}
		return 0, false, core.InternalError("backup: stat S3 object %q", key).Wrap(err)
	}
	return info.Size, true, nil
}

// DownloadToFile streams an object to dest, refusing to write more than max bytes
// to disk. It is the dump transport for the drop-off import: a large dump (up to
// the single-PUT ceiling) is streamed straight to disk and never buffered in
// memory — an improvement over the ssh-less import path, which reads the whole
// object into a []byte.
//
// minio's FGetObject is deliberately NOT used: it has no byte ceiling. A holder of
// the dump-key presigned PUT can swap a much larger object in AFTER the panel's
// StatObject pre-check (a TOCTOU), so the transfer itself — not a stale pre-stat —
// must be the authoritative size guard, exactly as GetObjectLimited is for the
// (small) meta.json. We copy at most max+1 bytes through an io.LimitReader; if the
// ceiling is reached the partial file is removed and the object is rejected, so a
// swapped-in giant dump can never exhaust the disk before the SHA-256 check runs.
// A missing key is mapped to CodeNotFound per the GetObject contract.
func (s *S3ObjectStore) DownloadToFile(ctx context.Context, key, dest string, max int64) error {
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return s.classifyGet(err, key)
	}
	defer func() { _ = obj.Close() }()

	f, err := os.Create(dest)
	if err != nil {
		return core.InternalError("backup: create download file %q", dest).Wrap(err)
	}
	// Copy one byte past the ceiling so an over-limit object is detectable without
	// writing it whole. minio's GetObject is lazy, so a missing key / transport
	// error surfaces here on the first read.
	n, copyErr := io.Copy(f, io.LimitReader(obj, max+1))
	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(dest)
		return s.classifyGet(copyErr, key)
	}
	if closeErr != nil {
		_ = os.Remove(dest)
		return core.InternalError("backup: finalize download file %q", dest).Wrap(closeErr)
	}
	if n > max {
		_ = os.Remove(dest)
		return core.ValidationError("backup: S3 object %q exceeds the %d-byte download limit", key, max)
	}
	return nil
}

// ProbePutReachable confirms the configured credentials can perform the FULL
// object lifecycle the panel will need BEFORE it hands a presigned-PUT command to a
// source it cannot otherwise help — so a misconfiguration fails the mint while the
// operator is still in the panel, instead of surfacing later on the hard-to-reach
// source as a misleading "the link may have expired", or worse, leaving an imported
// dump that can never be cleaned up.
//
// It is NOT enough to tolerate a PutObject-only policy: although the source only
// ever performs the single presigned PUT, the PANEL itself must later StatObject,
// GET/download and DeleteObject those same keys to import the dump and reclaim it.
// A PutObject-only policy would let the source's upload succeed yet leave the panel
// unable to import (stat/get denied) or clean up (delete denied) — orphaning a full
// database at rest forever. So the probe exercises exactly that lifecycle against a
// disposable probe object: PUT, then stat, then read, then delete. If any step is
// denied the mint fails NOW with a clear cause. The probe key is a fresh per-session
// throwaway (migrate.DropProbeKey); the deferred delete cleans it up on every path.
func (s *S3ObjectStore) ProbePutReachable(ctx context.Context, key string) error {
	payload := []byte("indiepg drop-off reachability probe")
	// Register the cleanup BEFORE the PUT and run it on a DETACHED, re-bounded context:
	// an ambiguous PUT (a timeout/cancellation where the object may actually have
	// landed) would otherwise strand an untracked probe object, and reusing the (by
	// then possibly cancelled) request context for cleanup would make the delete a
	// silent no-op. The delete is version-aware + idempotent, so cleaning up a
	// never-created object is harmless.
	defer func() {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_ = s.DeleteObject(cctx, key)
	}()
	if err := s.PutObject(ctx, key, payload); err != nil {
		return core.InternalError("backup: S3 is not writable for a presigned PUT to %q (check the bucket, endpoint and credentials)", key).Wrap(err)
	}

	if _, exists, err := s.StatObject(ctx, key); err != nil {
		return core.InternalError("backup: the configured S3 credentials cannot stat objects (needed to detect the upload and its size); grant s3:GetObject/HeadObject").Wrap(err)
	} else if !exists {
		return core.InternalError("backup: S3 probe object %q vanished immediately after a successful PUT — the bucket may not be durable or another writer is racing it", key)
	}
	if _, err := s.GetObjectLimited(ctx, key, int64(len(payload))+1); err != nil {
		return core.InternalError("backup: the configured S3 credentials cannot read objects (needed to download the dump); grant s3:GetObject").Wrap(err)
	}
	// Delete EXPLICITLY (not only via the deferred best-effort cleanup) so a
	// delete-denied policy fails the mint: without delete the panel could never
	// reclaim the multi-GiB dump after import, orphaning a full database forever.
	// On a versioned bucket DeleteObject purges all versions, so an Object-Lock /
	// retention bucket that cannot be purged also fails here.
	if err := s.DeleteObject(ctx, key); err != nil {
		return core.InternalError("backup: the configured S3 credentials cannot delete objects (needed to clean up the dump after import); grant s3:DeleteObject").Wrap(err)
	}

	// On a VERSIONED bucket, confirm the delete actually ERASED the object rather than
	// leaving data versions at rest: a full database dump that can never be purged
	// (versioning + Object Lock / a retention rule) is a real data-retention problem,
	// so fail the mint NOW instead of silently orphaning it after every import. The
	// explicit DeleteObject above already FAILS CLOSED when the versioning state cannot
	// be read, so reaching here means the lookup succeeded; a clean non-versioned
	// bucket simply skips this check.
	if versioned, verr := s.bucketVersioningEnabled(ctx); verr == nil && versioned {
		remaining, lerr := s.countDataVersions(ctx, key)
		if lerr != nil {
			return core.InternalError("backup: this S3 bucket has versioning enabled but its object versions cannot be listed (needed to purge an imported dump); grant s3:ListBucket / s3:ListBucketVersions, or use a non-versioned bucket for drop-off migrations").Wrap(lerr)
		}
		if remaining > 0 {
			return core.InternalError("backup: this S3 bucket retains object versions after deletion (versioning with Object Lock or a retention rule), so an uploaded database dump would persist indefinitely after cleanup — use a bucket without Object Lock / mandatory retention for drop-off migrations")
		}
	}
	return nil
}

// classifyGet maps a minio "not found" to CodeNotFound (per the ObjectStore
// contract) and anything else to an internal error.
func (s *S3ObjectStore) classifyGet(err error, key string) error {
	resp := minio.ToErrorResponse(err)
	if resp.StatusCode == http.StatusNotFound || resp.Code == "NoSuchKey" || resp.Code == "NoSuchBucket" {
		return core.NotFoundError("backup: S3 object %q not found", key).Wrap(err)
	}
	return core.InternalError("backup: read S3 object %q", key).Wrap(err)
}
