// Package hespera is the module root. Its only job is to embed the web UI
// assets (server-rendered html/template pages, partials, and the vendored
// static JS/CSS) so the whole server ships as one self-contained binary — no
// loose web/ directory to deploy alongside it, the way the Docker image had to.
package hespera

import (
	"embed"
	"io/fs"
)

//go:embed all:web
var webFS embed.FS

// WebFS returns the asset tree rooted at the web/ directory, so "templates/…"
// and "static/…" resolve directly. Templates are parsed from it and the static
// handler serves a "static" sub-tree of it.
func WebFS() fs.FS {
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		panic(err) // embed guarantees web/ exists; a failure here is a build bug
	}
	return sub
}
