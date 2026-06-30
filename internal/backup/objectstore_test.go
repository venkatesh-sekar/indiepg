package backup

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
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

// TestSelectVersionIDs pins the pure version-selection logic behind the durable
// (versioned-bucket) delete: a version listing is taken with a Prefix, so it also
// yields keys that merely START with the target key — those must be left alone — and
// delete markers ARE removable versions that must be purged so the key leaves no
// trace.
func TestSelectVersionIDs(t *testing.T) {
	key := "pg-migrations/dropoff/ABCDEF/dump"
	infos := []minio.ObjectInfo{
		{Key: key, VersionID: "v1"},
		{Key: key, VersionID: "dm1", IsDeleteMarker: true}, // a delete marker is still a version to purge
		{Key: key, VersionID: "v2"},
		{Key: key + "-sibling", VersionID: "vX"}, // prefix match for a DIFFERENT key: never touched
		{Key: "other/object", VersionID: "vY"},
	}
	got := selectVersionIDs(key, infos)
	require.Equal(t, []string{"v1", "dm1", "v2"}, got,
		"every exact-key version (incl. delete markers) is purged; a sibling/other key is left alone")
}

// versionsXML renders a ListObjectVersions response carrying three data versions and
// a delete marker for key, plus one version of a sibling key (prefix-match) the purge
// must NOT touch. Only tags minio's ListVersionsResult unmarshaler recognises appear.
func versionsXML(key string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>` +
		`<ListVersionsResult><Name>drops</Name><Prefix>` + key + `</Prefix>` +
		`<KeyMarker></KeyMarker><VersionIdMarker></VersionIdMarker>` +
		`<MaxKeys>1000</MaxKeys><IsTruncated>false</IsTruncated>` +
		`<Version><Key>` + key + `</Key><VersionId>v1</VersionId></Version>` +
		`<Version><Key>` + key + `</Key><VersionId>v2</VersionId></Version>` +
		`<DeleteMarker><Key>` + key + `</Key><VersionId>dm1</VersionId></DeleteMarker>` +
		`<Version><Key>` + key + `</Key><VersionId>v3</VersionId></Version>` +
		`<Version><Key>` + key + `-sibling</Key><VersionId>vX</VersionId></Version>` +
		`</ListVersionsResult>`
}

// newStubStore builds an S3ObjectStore pointed at a path-style httptest endpoint.
func newStubStore(t *testing.T, rawURL string) *S3ObjectStore {
	t.Helper()
	s, err := NewS3ObjectStore(S3StoreParams{
		Endpoint:  rawURL,
		Region:    "us-east-1",
		Bucket:    "drops",
		AccessKey: "AK",
		SecretKey: "SK",
		UseSSL:    false,
		PathStyle: true,
	})
	require.NoError(t, err)
	return s
}

// TestDeleteObjectPurgesAllVersionsOnVersionedBucket pins finding #1: on a bucket
// with versioning enabled a plain RemoveObject only writes a delete marker and leaves
// the dump's data versions at rest forever, so DeleteObject must instead purge EVERY
// version of the key by VersionID. It also pins the versioning-state caching.
func TestDeleteObjectPurgesAllVersionsOnVersionedBucket(t *testing.T) {
	const key = "pg-migrations/dropoff/ABCDEF/dump"

	var (
		mu              sync.Mutex
		versioningHits  int
		deletedVersions []string
		plainDeletes    int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case q.Has("versioning"):
			mu.Lock()
			versioningHits++
			mu.Unlock()
			w.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><VersioningConfiguration><Status>Enabled</Status></VersioningConfiguration>`)
		case q.Has("versions"):
			w.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(w, versionsXML(key))
		case r.Method == http.MethodDelete:
			vid := q.Get("versionId")
			mu.Lock()
			if vid == "" {
				plainDeletes++
			} else {
				deletedVersions = append(deletedVersions, vid)
			}
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	s := newStubStore(t, srv.URL)
	require.NoError(t, s.DeleteObject(context.Background(), key))
	// A second delete must reuse the CACHED versioning state (no second lookup).
	require.NoError(t, s.DeleteObject(context.Background(), key))

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, 1, versioningHits, "bucket versioning is looked up once and cached")
	require.Zero(t, plainDeletes, "a versioned bucket must never use a marker-only plain delete")
	// Two DeleteObject calls, each purging the 4 exact-key versions (3 data + 1 marker);
	// the sibling key's version (vX) is never deleted.
	require.ElementsMatch(t,
		[]string{"v1", "v2", "dm1", "v3", "v1", "v2", "dm1", "v3"}, deletedVersions,
		"every exact-key version is removed by VersionID; the prefix-sibling is untouched")
}

// TestDeleteObjectPlainDeleteOnUnversionedBucket proves the common path is unchanged:
// a non-versioned bucket uses a single plain delete and never lists or version-deletes
// (so a minimal PutObject/GetObject/DeleteObject policy without ListBucket still works).
func TestDeleteObjectPlainDeleteOnUnversionedBucket(t *testing.T) {
	const key = "pg-migrations/dropoff/ABCDEF/dump"

	var (
		mu             sync.Mutex
		listHits       int
		plainDeletes   int
		versionDeletes int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case q.Has("versioning"):
			w.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><VersioningConfiguration></VersioningConfiguration>`)
		case q.Has("versions"):
			mu.Lock()
			listHits++
			mu.Unlock()
			w.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(w, versionsXML(key))
		case r.Method == http.MethodDelete:
			mu.Lock()
			if q.Get("versionId") == "" {
				plainDeletes++
			} else {
				versionDeletes++
			}
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	s := newStubStore(t, srv.URL)
	require.NoError(t, s.DeleteObject(context.Background(), key))

	mu.Lock()
	defer mu.Unlock()
	require.Zero(t, listHits, "a non-versioned bucket never lists versions")
	require.Zero(t, versionDeletes, "a non-versioned bucket never deletes by VersionID")
	require.Equal(t, 1, plainDeletes, "a non-versioned bucket uses a single plain delete")
}

// TestProbePutReachable pins the FULL-lifecycle reachability probe (finding #5):
// the probe must exercise exactly the object operations the PANEL later needs — PUT,
// then stat (HEAD), then read (GET), then delete — and fail the mint if ANY of them
// is denied. A PutObject-only policy (the source can upload but the panel cannot
// stat/read/delete) is NOT acceptable: it would import-fail or orphan the dump with
// no cleanup. Only when the whole lifecycle succeeds is the target reachable.
func TestProbePutReachable(t *testing.T) {
	const probeKey = "pg-migrations/dropoff/ABCDEF/.probe"

	// statuses maps an HTTP method to the status the stub returns; a method not
	// present defaults to a successful response, so a case only lists the denials it
	// models. errCode is the S3 <Code> placed in the (non-HEAD) error body.
	type lifecycle struct {
		name      string
		statuses  map[string]int
		errCode   string
		reachable bool
	}
	cases := []lifecycle{
		{"full lifecycle granted", map[string]int{}, "", true},
		{"PutObject-only: stat/read denied", map[string]int{
			http.MethodHead: http.StatusForbidden,
			http.MethodGet:  http.StatusForbidden,
		}, "AccessDenied", false},
		{"delete denied (cannot clean up)", map[string]int{
			http.MethodDelete: http.StatusForbidden,
		}, "AccessDenied", false},
		{"put denied (bad credentials)", map[string]int{
			http.MethodPut: http.StatusForbidden,
		}, "SignatureDoesNotMatch", false},
		{"missing bucket on put", map[string]int{
			http.MethodPut: http.StatusNotFound,
		}, "NoSuchBucket", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				st, listed := tc.statuses[r.Method]
				if !listed {
					st = http.StatusOK // default: this op is granted
				}
				ok := st == http.StatusOK || st == http.StatusNoContent
				switch {
				case r.Method == http.MethodPut && ok:
					w.Header().Set("ETag", `"probe-etag"`)
					w.WriteHeader(http.StatusOK)
					return
				case r.Method == http.MethodHead && ok:
					w.Header().Set("Content-Length", "35")
					w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
					w.Header().Set("ETag", `"probe-etag"`)
					w.WriteHeader(http.StatusOK)
					return
				case r.Method == http.MethodGet && ok:
					w.Header().Set("Content-Length", "35")
					w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
					w.Header().Set("ETag", `"probe-etag"`)
					w.WriteHeader(http.StatusOK)
					_, _ = io.WriteString(w, "indiepg drop-off reachability probe")
					return
				case r.Method == http.MethodDelete && ok:
					w.WriteHeader(http.StatusNoContent)
					return
				}
				// Denied: an XML <Error> body for GET/PUT/DELETE (minio parses the
				// <Code>); HEAD carries no body, so minio synthesizes the error itself.
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(st)
				if tc.errCode != "" && r.Method != http.MethodHead {
					_, _ = io.WriteString(w,
						`<?xml version="1.0" encoding="UTF-8"?><Error><Code>`+tc.errCode+
							`</Code><Message>`+tc.errCode+`</Message></Error>`)
				}
			}))
			defer srv.Close()

			// srv.URL is http://127.0.0.1:port; NewS3ObjectStore strips the scheme.
			s, err := NewS3ObjectStore(S3StoreParams{
				Endpoint:  srv.URL,
				Region:    "us-east-1",
				Bucket:    "drops",
				AccessKey: "AK",
				SecretKey: "SK",
				UseSSL:    false,
				PathStyle: true,
			})
			require.NoError(t, err)

			perr := s.ProbePutReachable(context.Background(), probeKey)
			if tc.reachable {
				require.NoError(t, perr, "must treat %s as reachable", tc.name)
			} else {
				require.Error(t, perr, "must treat %s as NOT reachable", tc.name)
				require.Equal(t, core.CodeInternal, core.CodeOf(perr))
			}
		})
	}
}
