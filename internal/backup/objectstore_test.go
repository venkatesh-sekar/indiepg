package backup

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/venkatesh-sekar/indiepg/internal/core"
)

func TestNewS3ObjectStoreRequiresEndpointAndBucket(t *testing.T) {
	_, err := NewS3ObjectStore(S3StoreParams{Bucket: "b"})
	require.Error(t, err)
	require.Equal(t, core.CodeValidation, core.CodeOf(err))

	_, err = NewS3ObjectStore(S3StoreParams{Endpoint: "s3.example.com"})
	require.Error(t, err)
	require.Equal(t, core.CodeValidation, core.CodeOf(err))
}

func TestNewS3ObjectStoreNormalizesEndpoint(t *testing.T) {
	// A pasted console URL (scheme + trailing slash) must still construct.
	s, err := NewS3ObjectStore(S3StoreParams{
		Endpoint:  "https://s3.us-west-002.backblazeb2.com/",
		Region:    "us-west-002",
		Bucket:    "my-backups",
		AccessKey: "key",
		SecretKey: "secret",
		UseSSL:    true,
	})
	require.NoError(t, err)
	require.NotNil(t, s)
	require.Equal(t, "my-backups", s.bucket)
}

// newPresignStore builds an S3ObjectStore pointed at a stub endpoint. PresignPut
// is a pure local signing operation (no network), so it can be unit-tested.
func newPresignStore(t *testing.T) *S3ObjectStore {
	t.Helper()
	s, err := NewS3ObjectStore(S3StoreParams{
		Endpoint:  "s3.us-west-002.backblazeb2.com",
		Region:    "us-west-002",
		Bucket:    "drops",
		AccessKey: "AKIAEXAMPLE",
		SecretKey: "secretkey",
		UseSSL:    true,
	})
	require.NoError(t, err)
	return s
}

// TestPresignPut_signsPutURLForKey proves PresignPut mints a PUT-scoped, single-
// key, signed URL — the drop-off transport — without contacting S3.
func TestPresignPut_signsPutURLForKey(t *testing.T) {
	s := newPresignStore(t)
	key := "pg-migrations/dropoff/ABCDEF/dump"
	url, err := s.PresignPut(context.Background(), key, 2*time.Hour)
	require.NoError(t, err)
	require.Contains(t, url, key, "URL must target exactly the requested key")
	require.Contains(t, url, "drops", "URL must address the configured bucket")
	require.Contains(t, url, "X-Amz-Signature=", "URL must be V4-presigned")
	require.True(t, strings.HasPrefix(url, "https://"), "TLS endpoint must presign to https")
}

// TestPresignPut_clampsTTL ensures a ttl outside minio's accepted 1s..7d window is
// clamped rather than rejected, so an out-of-range caller never gets an error.
func TestPresignPut_clampsTTL(t *testing.T) {
	s := newPresignStore(t)
	ctx := context.Background()

	// Below 1s: clamped up, still signs.
	url, err := s.PresignPut(ctx, "k1", 0)
	require.NoError(t, err)
	require.Contains(t, url, "X-Amz-Signature=")

	// Above 7 days: clamped down, still signs.
	url, err = s.PresignPut(ctx, "k2", 30*24*time.Hour)
	require.NoError(t, err)
	require.Contains(t, url, "X-Amz-Signature=")
}
