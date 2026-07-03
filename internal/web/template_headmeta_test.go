package web

import (
	"bytes"
	"html/template"
	"path/filepath"
	"strings"
	"testing"
)

// TestHeadMetaNoPreview guards the Turbo cached-preview fix: media-boot pages
// (whose inline body script auto-starts a <video>/<audio> on render) must
// override the layout's "headmeta" block with
// <meta name="turbo-cache-control" content="no-preview"> so Turbo never renders
// them as a cached preview (a preview re-runs the body script on a throwaway DOM
// and orphans a still-playing media element). Non-media pages must keep the
// layout's empty default — the meta must NOT leak onto them.
//
// It compiles the REAL templates (the handler test harness elsewhere uses stubs,
// which can't exercise this), parsing each page exactly like Handler.New does,
// and renders the "headmeta" template alone — it's an independent define, so no
// page data is needed.
func TestHeadMetaNoPreview(t *testing.T) {
	const noPreview = `content="no-preview"`
	tplDir := filepath.Join("..", "..", "web", "templates")
	layoutPath := filepath.Join(tplDir, "layout.html")

	render := func(t *testing.T, page string) string {
		t.Helper()
		// Mirror Handler.New: layout base (with the staticv FuncMap) + the page.
		base := template.New("layout.html").Funcs(template.FuncMap{
			"staticv": func(p string) string { return p },
			"initial": initialRune,
		})
		tpl, err := base.ParseFiles(layoutPath, filepath.Join(tplDir, page))
		if err != nil {
			t.Fatalf("ParseFiles(%s): %v", page, err)
		}
		var buf bytes.Buffer
		if err := tpl.ExecuteTemplate(&buf, "headmeta", nil); err != nil {
			t.Fatalf("ExecuteTemplate headmeta (%s): %v", page, err)
		}
		return buf.String()
	}

	// Media-boot pages opt out of preview.
	for _, page := range []string{"tv_player.html"} {
		if got := render(t, page); !strings.Contains(got, noPreview) {
			t.Errorf("%s: headmeta missing the no-preview opt-out; got %q", page, got)
		}
	}

	// Non-media pages keep the empty default — the meta must not leak.
	for _, page := range []string{"home.html", "tv_season.html"} {
		if got := render(t, page); strings.Contains(got, noPreview) {
			t.Errorf("%s: headmeta unexpectedly contains the no-preview meta; got %q", page, got)
		}
	}
}
