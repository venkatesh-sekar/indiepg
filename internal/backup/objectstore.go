package backup

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/identity"
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

// classifyGet maps a minio "not found" to CodeNotFound (per the ObjectStore
// contract) and anything else to an internal error.
func (s *S3ObjectStore) classifyGet(err error, key string) error {
	resp := minio.ToErrorResponse(err)
	if resp.StatusCode == http.StatusNotFound || resp.Code == "NoSuchKey" || resp.Code == "NoSuchBucket" {
		return core.NotFoundError("backup: S3 object %q not found", key).Wrap(err)
	}
	return core.InternalError("backup: read S3 object %q", key).Wrap(err)
}
