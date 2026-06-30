package backup

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"strings"
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

// DeleteObject removes the object. A missing object is not an error.
func (s *S3ObjectStore) DeleteObject(ctx context.Context, key string) error {
	if err := s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return core.InternalError("backup: delete S3 object %q", key).Wrap(err)
	}
	return nil
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
	if err := s.PutObject(ctx, key, payload); err != nil {
		return core.InternalError("backup: S3 is not writable for a presigned PUT to %q (check the bucket, endpoint and credentials)", key).Wrap(err)
	}
	// Best-effort cleanup of the probe object no matter which step below fails, so a
	// probe that aborts after the PUT never strands the throwaway object.
	defer func() { _ = s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{}) }()

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
	if err := s.DeleteObject(ctx, key); err != nil {
		return core.InternalError("backup: the configured S3 credentials cannot delete objects (needed to clean up the dump after import); grant s3:DeleteObject").Wrap(err)
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
