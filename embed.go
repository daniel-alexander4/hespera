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

// Verbatim third-party license texts (third_party/licenses/README.md). MIT/
// BSD-style licenses require reproducing their text with binary
// distributions, and the primary artifact is a bare self-contained binary —
// so the texts travel inside it, served at /about/licenses.
//
//go:embed all:third_party
var thirdPartyFS embed.FS

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

// ThirdPartyLicensesFS returns the vendored license-text tree rooted at
// third_party/licenses (one directory per module path).
func ThirdPartyLicensesFS() fs.FS {
	sub, err := fs.Sub(thirdPartyFS, "third_party/licenses")
	if err != nil {
		panic(err) // embed guarantees the dir exists; a failure here is a build bug
	}
	return sub
}
