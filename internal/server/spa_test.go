package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"
)

// testDist builds an in-memory SPA filesystem mirroring a real build output:
// an index.html shell plus a fingerprinted asset under assets/.
func testDist() fstest.MapFS {
	return fstest.MapFS{
		"index.html":            {Data: []byte("<!doctype html><div id=root>app</div>")},
		"assets/app-abc123.js":  {Data: []byte("console.log('hi')")},
		"assets/app-abc123.css": {Data: []byte("body{}")},
		"favicon.ico":           {Data: []byte("icon")},
	}
}

func TestSPAServesIndexAtRoot(t *testing.T) {
	h := newSPAHandler(testDist())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "id=root")
	require.Equal(t, "no-cache", rec.Header().Get("Cache-Control"))
}

func TestSPAServesRealAsset(t *testing.T) {
	h := newSPAHandler(testDist())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/assets/app-abc123.js", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "console.log")
	require.Contains(t, rec.Header().Get("Cache-Control"), "immutable")
}

func TestSPAFallsBackToIndexForUnknownRoute(t *testing.T) {
	h := newSPAHandler(testDist())
	rec := httptest.NewRecorder()
	// A client-side route that is not a real file should yield the shell.
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/databases", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "id=root")
	require.Equal(t, "no-cache", rec.Header().Get("Cache-Control"))
}

func TestSPANonImmutableRootFileNotCached(t *testing.T) {
	h := newSPAHandler(testDist())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/favicon.ico", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "no-cache", rec.Header().Get("Cache-Control"))
}

func TestSPARejectsNonGet(t *testing.T) {
	h := newSPAHandler(testDist())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestSPAPathTraversalServesIndex(t *testing.T) {
	h := newSPAHandler(testDist())
	rec := httptest.NewRecorder()
	// path.Clean collapses traversal; the cleaned path is not a real file so
	// the shell is served (never escaping the embedded FS).
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/../../etc/passwd", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "id=root")
}

func TestIsImmutableAsset(t *testing.T) {
	require.True(t, isImmutableAsset("assets/app.js"))
	require.True(t, isImmutableAsset("static/x.css"))
	require.False(t, isImmutableAsset("index.html"))
	require.False(t, isImmutableAsset("favicon.ico"))
}
