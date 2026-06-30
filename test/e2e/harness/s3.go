//go:build e2e

package harness

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// S3 is MinIO ground truth for backup/migration assertions. It talks to MinIO on
// the published host port over verified TLS (the e2e CA is loaded into a private
// transport, so no skip-verify), path-style, with the fixed e2e credentials.
type S3 struct {
	client *minio.Client
	bucket string
}

// newS3 builds the host-side MinIO client for endpoint host:port, trusting the
// e2e CA (its SAN includes 127.0.0.1) so verification succeeds against the
// loopback-published port.
func newS3(endpoint, bucket string) (*S3, error) {
	caPEM, err := os.ReadFile(caCertPath())
	if err != nil {
		return nil, fmt.Errorf("read e2e CA %s: %w", caCertPath(), err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("e2e CA %s is not valid PEM", caCertPath())
	}
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool},
	}
	client, err := minio.New(endpoint, &minio.Options{
		Creds:        credentials.NewStaticV4(MinIOAccessKey, MinIOSecretKey, ""),
		Secure:       true,
		Region:       "us-east-1",
		BucketLookup: minio.BucketLookupPath, // MinIO needs path-style
		Transport:    transport,
	})
	if err != nil {
		return nil, err
	}
	return &S3{client: client, bucket: bucket}, nil
}

// Bucket returns the backup bucket name.
func (s *S3) Bucket() string { return s.bucket }

// EnsureBucket creates the bucket if it does not already exist (idempotent).
func (s *S3) EnsureBucket() error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	exists, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	return s.client.MakeBucket(ctx, s.bucket, minio.MakeBucketOptions{Region: "us-east-1"})
}

// ListObjects returns every object key under prefix (recursive).
func (s *S3) ListObjects(prefix string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var keys []string
	for obj := range s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true}) {
		if obj.Err != nil {
			return nil, obj.Err
		}
		keys = append(keys, obj.Key)
	}
	return keys, nil
}

// CountObjects returns how many objects exist under prefix.
func (s *S3) CountObjects(prefix string) (int, error) {
	keys, err := s.ListObjects(prefix)
	if err != nil {
		return 0, err
	}
	return len(keys), nil
}

// ObjectExists reports whether an exact key exists.
func (s *S3) ObjectExists(key string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, err := s.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	if err == nil {
		return true, nil
	}
	if minio.ToErrorResponse(err).StatusCode == http.StatusNotFound {
		return false, nil
	}
	return false, err
}
