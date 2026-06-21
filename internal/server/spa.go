package server

import (
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// spaHandler serves the embedded single-page app. Requests for real files
// (assets, index.html) are served directly; any other GET that is not an API
// path falls back to index.html so client-side routing works on deep links and
// hard refreshes.
type spaHandler struct {
	fsys    fs.FS
	fileSrv http.Handler
}

// newSPAHandler builds an spaHandler over the embedded dist filesystem.
func newSPAHandler(fsys fs.FS) *spaHandler {
	return &spaHandler{
		fsys:    fsys,
		fileSrv: http.FileServer(http.FS(fsys)),
	}
}

// ServeHTTP serves a static asset if it exists, otherwise the SPA index for
// client-side routing. Only GET/HEAD are served; other methods 404 (the API
// owns mutating verbs under /api).
func (h *spaHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.NotFound(w, r)
		return
	}

	upath := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if upath == "" {
		upath = "index.html"
	}

	if h.exists(upath) {
		// Long-cache fingerprinted assets; never cache the HTML shell so a
		// deploy is picked up immediately.
		if isImmutableAsset(upath) {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		h.fileSrv.ServeHTTP(w, r)
		return
	}

	// SPA fallback: serve index.html for unknown non-asset paths.
	h.serveIndex(w, r)
}

// exists reports whether the named file is present in the embedded FS and is
// not a directory.
func (h *spaHandler) exists(name string) bool {
	f, err := h.fsys.Open(name)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		return false
	}
	return true
}

// serveIndex writes index.html with no-cache so navigations always get the
// latest shell.
func (h *spaHandler) serveIndex(w http.ResponseWriter, r *http.Request) {
	f, err := h.fsys.Open("index.html")
	if err != nil {
		http.Error(w, "SPA not built", http.StatusNotFound)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		http.Error(w, "SPA not built", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if rs, ok := f.(io.ReadSeeker); ok {
		http.ServeContent(w, r, "index.html", info.ModTime(), rs)
		return
	}
	// Fallback: stream without range support.
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, f)
}

// isImmutableAsset reports whether the path looks like a build-fingerprinted
// asset safe to cache forever (anything under assets/, or with a content hash
// in its name). Conservative: only cache the common build output dir.
func isImmutableAsset(name string) bool {
	return strings.HasPrefix(name, "assets/") || strings.HasPrefix(name, "static/")
}
