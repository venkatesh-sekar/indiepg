package backup

import (
	"testing"

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
