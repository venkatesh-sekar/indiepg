//go:build e2e

package harness

import (
	"bytes"
	"context"
	"io"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
)

// s3_extra.go adds raw object read/write helpers on *S3 that the Wave-2
// single-writer ownership scenario needs to manipulate the repo DIRECTLY,
// bypassing the panel — e.g. to plant a FOREIGN ownership marker so we can prove
// the panel refuses to write to a repo it does not own. These are additive
// methods on the frozen *S3 type (Go allows a type's methods to span files in
// the same package); the frozen s3.go is untouched.

// PutObject writes raw bytes to key in the backup bucket, overwriting any object
// already there. The ownership scenario uses it to overwrite the panel's
// .panel-owner.json marker with a foreign owner's document.
func (s *S3) PutObject(key string, data []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := s.client.PutObject(ctx, s.bucket, key, bytes.NewReader(data), int64(len(data)),
		minio.PutObjectOptions{ContentType: "application/json"})
	return err
}

// GetObject reads the full bytes of key from the backup bucket.
func (s *S3) GetObject(key string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	defer func() { _ = obj.Close() }()
	return io.ReadAll(obj)
}

// FindObjectWithSuffix returns the first object key under prefix (recursive)
// whose key ends with suffix, or "" if none match. The ownership scenario uses
// it to locate the .panel-owner.json marker without hard-coding the repo prefix.
func (s *S3) FindObjectWithSuffix(prefix, suffix string) (string, error) {
	keys, err := s.ListObjects(prefix)
	if err != nil {
		return "", err
	}
	for _, k := range keys {
		if strings.HasSuffix(k, suffix) {
			return k, nil
		}
	}
	return "", nil
}
