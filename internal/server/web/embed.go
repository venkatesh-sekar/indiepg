// Package web embeds the compiled SPA so the server ships as a single binary
// with no Node runtime. The dist/ directory is produced by `make web`; a
// placeholder index.html is committed so the package compiles before the real
// SPA is built.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var distFS embed.FS

// DistFS returns the embedded SPA file system rooted at the dist directory, so
// callers see "index.html" at the root rather than "dist/index.html".
func DistFS() (fs.FS, error) {
	return fs.Sub(distFS, "dist")
}
