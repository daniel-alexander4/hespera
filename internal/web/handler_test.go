package web

import (
	"database/sql"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	isodb "hespera/internal/db"

	"hespera/internal/config"
)

// openTestDB creates a temp SQLite database with migrations applied.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.sqlite")
	conn, err := isodb.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := isodb.Migrate(conn); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return conn
}

// stubPages mirrors the page list New() compiles — kept here so the stub asset
// FS provides every one (a missing page would fail New()).
var stubPages = []string{
	"home.html", "libraries_new.html", "integrity_report.html",
	"settings.html", "music_home.html", "music_artist.html",
	"music_artist_external.html", "music_artist_disambiguate.html", "music_artist_art.html",
	"music_album.html", "music_albums.html", "music_compilations.html", "music_playlist.html", "player.html",
	"music_match_review.html", "music_album_edit.html", "music_track_edit.html", "music_duplicates.html",
	"settings_tags.html", "tv_home.html", "tv_series.html",
	"tv_season.html", "tv_match_review.html", "tv_player.html", "person.html",
	"photos_home.html", "photo_view.html", "photo_player.html",
	"books_home.html", "book_view.html", "book_reader.html",
	"movies_home.html", "movie_detail.html", "movie_match_review.html", "movie_player.html",
}

// stubAssetsFS builds an in-memory web asset tree (rooted like the embedded one:
// templates/ + static/) with minimal functional template stubs. Handler-logic
// tests run against these stubs so they stay decoupled from the real template
// HTML; the few real-template tests use the embedded assets (New with no
// AssetsFS). Returns an fs.FS suitable for Deps.AssetsFS.
func stubAssetsFS() fs.FS {
	const layout = `{{define "layout.html"}}<!DOCTYPE html><html><body>{{template "content" .}}</body></html>{{end}}`
	const pageContent = `{{define "content"}}hello{{end}}`

	m := fstest.MapFS{
		"templates/layout.html": &fstest.MapFile{Data: []byte(layout)},
		// A partials file so the "templates/partials_*.html" glob matches.
		"templates/partials_stub.html": &fstest.MapFile{Data: []byte(`{{define "partial-stub"}}{{end}}`)},
		// A static entry so fs.Sub(webRoot, "static") has a populated subtree.
		"static/.keep": &fstest.MapFile{Data: []byte("")},
	}
	for _, p := range stubPages {
		m["templates/"+p] = &fstest.MapFile{Data: []byte(pageContent)}
	}

	// Functional overrides: stubs that render the same data the handler passes,
	// so tests can assert the wiring.
	overrides := map[string]string{
		"templates/music_match_review.html": `{{define "content"}}` +
			`{{if .Albums}}{{if .LibraryID}}<button>Run Match</button>{{end}}` +
			`{{range .Albums}}<span>{{.Title}}</span>{{end}}` +
			`{{else}}<p>No albums need review</p>{{end}}{{end}}`,
		"templates/tv_match_review.html": `{{define "content"}}` +
			`{{if .Groups}}{{range .Groups}}<span>{{.GuessedTitle}}</span>{{end}}` +
			`{{else}}<p>No series need review</p>{{end}}{{end}}`,
		"templates/movie_match_review.html": `{{define "content"}}` +
			`{{if .Groups}}{{range .Groups}}<span>{{.GuessedTitle}}</span>{{end}}` +
			`{{else}}<p>No movies need review</p>{{end}}{{end}}`,
		// The settings page hosts the accordion cards; its stub carries the
		// jobs-container define (settingsJobsFragment renders it) and the
		// libraries pills the integrity tests assert.
		"templates/settings.html": `{{define "content"}}<div id="jobs-container">{{template "jobs-container" .}}</div>` +
			`{{range .Libraries}}<div class="lib">{{.Name}}` +
			`{{$f := index $.Flagged .ID}}{{if $f}}<a href="/libraries/integrity-report?id={{.ID}}" class="badge badge-warn">{{$f}} corrupt</a>{{end}}` +
			`{{$d := index $.Degraded .ID}}{{if $d}}<a href="/libraries/integrity-report?id={{.ID}}" class="badge badge-neutral">{{$d}} degraded</a>{{end}}` +
			`</div>{{end}}{{end}}` +
			`{{define "jobs-container"}}{{if .Jobs}}<table>{{range .Jobs}}<tr><td>{{.JobType}}</td>` +
			`<td><span class="badge badge-{{.Status}}">{{.Status}}</span></td></tr>{{end}}</table>` +
			`{{else}}<p>No jobs found.</p>{{end}}{{end}}`,
		"templates/player.html": `{{define "content"}}<div class="player-page" data-autoload="{{.AutoloadQuery}}"></div>{{end}}`,
		"templates/music_albums.html": `{{define "content"}}{{range .Albums}}<a class="album" href="/music/album/{{.ID}}"></a>{{end}}` +
			`<span id="pg">{{.Page.Page}}/{{.Page.TotalPages}}</span>` +
			`{{if .Page.HasPrev}}<a class="prev"></a>{{end}}{{if .Page.HasNext}}<a class="next"></a>{{end}}{{end}}`,
		// Books wiring: the grid (full page + ?grid=1 fragment share the
		// book-cards define), the detail's Read/Resume state, and the reader's
		// data attributes.
		"templates/books_home.html": `{{define "content"}}{{template "book-cards" .}}` +
			`<span id="pg">{{.Page.Page}}/{{.Page.TotalPages}}</span>{{end}}` +
			`{{define "book-cards"}}{{range .Cards}}<a class="book" href="/books/view?id={{.ID}}">{{.Title}}</a>{{end}}{{end}}`,
		"templates/book_view.html": `{{define "content"}}<h1>{{.BookTitle}}</h1><p class="author">{{.Author}}</p>` +
			`{{if .Readable}}<a class="read" href="/book/reader?id={{.ID}}">{{if .HasProgress}}Resume {{.UnitName}} {{.AtUnit}}{{else}}Read{{end}}</a>{{end}}{{end}}`,
		"templates/book_reader.html": `{{define "content"}}<div id="bookReader" data-kind="{{.Kind}}" ` +
			`data-start-index="{{.StartIndex}}" data-start-fraction="{{.StartFraction}}" data-entries="{{.EntriesJSON}}"></div>{{end}}`,
		// Integrity-badge wiring: render the flag fields the detail handlers pass.
		"templates/tv_season.html": `{{define "content"}}{{range .Episodes}}<div class="ep">{{.EpisodeNumber}}` +
			`{{if .Flagged}}<span class="badge badge-warn" title="{{.FlagDetail}}">corrupt</span>{{end}}</div>{{end}}{{end}}`,
		"templates/tv_series.html": `{{define "content"}}{{range .Seasons}}<div class="season">{{.Name}}` +
			`{{if .FlaggedCount}}<span class="badge badge-warn">{{.FlaggedCount}} corrupt</span>{{end}}</div>{{end}}{{end}}`,
		"templates/movie_detail.html": `{{define "content"}}<h1>{{.MovieTitle}}</h1>` +
			`{{if .Flagged}}<span class="badge badge-warn" title="{{.FlagDetail}}">corrupt</span>{{end}}{{end}}`,
		"templates/tv_player.html": `{{define "content"}}<video data-file-id="{{.FileID}}" ` +
			`data-prev-file="{{.PrevFileID}}" data-next-file="{{.NextFileID}}"></video>{{end}}`,
		"templates/music_album.html": `{{define "content"}}{{range .DiscTracks}}{{range .Tracks}}<li>{{.Title}}` +
			`{{if .Flagged}}<span class="badge badge-warn" title="{{.FlagDetail}}">corrupt</span>{{end}}</li>{{end}}{{end}}{{end}}`,
		// Integrity report wiring: both severity sections + the per-row fields
		// + the cap-with-count notice.
		"templates/integrity_report.html": `{{define "content"}}` +
			`{{if gt .Total .Shown}}<p>Showing the first {{.Shown}} of {{.Total}} damaged files.</p>{{end}}` +
			`{{if .Flagged}}<h2>Corrupt — needs replacement ({{len .Flagged}})</h2>{{end}}` +
			`{{if .Degraded}}<h2>Degraded — playable with known defects ({{len .Degraded}})</h2>{{end}}` +
			`{{range .Flagged}}{{template "irow" .}}{{end}}{{range .Degraded}}{{template "irow" .}}{{end}}{{end}}` +
			`{{define "irow"}}<div class="irow">{{if .Href}}<a href="{{.Href}}">{{.Title}}</a>{{else}}{{.Title}}{{end}}` +
			`<code>{{.Path}}</code><span>{{humanBytes .SizeBytes}}</span>` +
			`<em>{{.Detail}}</em><p>{{.Mitigation}}</p></div>{{end}}`,
	}
	for path, content := range overrides {
		m[path] = &fstest.MapFile{Data: []byte(content)}
	}
	return m
}

// newTestHandler creates a Handler backed by a real SQLite DB with migrations
// applied and the in-memory stub asset tree (no chdir, no on-disk templates).
func newTestHandler(t *testing.T) (*Handler, *sql.DB) {
	t.Helper()
	dir := t.TempDir()
	db := openTestDB(t)
	h, err := New(Deps{
		Cfg:      config.Config{DataDir: dir, MediaRoot: dir},
		DB:       db,
		AssetsFS: stubAssetsFS(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return h, db
}

func TestNewValidTemplates(t *testing.T) {
	db := openTestDB(t)
	h, err := New(Deps{
		Cfg:      config.Config{DataDir: t.TempDir(), MediaRoot: t.TempDir()},
		DB:       db,
		AssetsFS: stubAssetsFS(),
	})
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	if h == nil {
		t.Fatal("New() returned nil handler")
	}
	// Verify all page templates are compiled
	expectedPages := 35
	if len(h.tpls) != expectedPages {
		t.Fatalf("expected %d templates, got %d", expectedPages, len(h.tpls))
	}
}

// stubAssetsWithout returns the stub asset FS with the given asset paths
// removed (or, for layout, replaced by broken syntax), to exercise New()'s
// template-compilation error paths.
func stubAssetsWithout(remove ...string) fstest.MapFS {
	base := stubAssetsFS().(fstest.MapFS)
	m := fstest.MapFS{}
	for k, v := range base {
		m[k] = v
	}
	for _, p := range remove {
		delete(m, "templates/"+p)
	}
	return m
}

func TestNewMissingLayout(t *testing.T) {
	_, err := New(Deps{
		Cfg:      config.Config{DataDir: t.TempDir(), MediaRoot: t.TempDir()},
		AssetsFS: stubAssetsWithout("layout.html"),
	})
	if err == nil {
		t.Fatal("New() should return error for missing layout")
	}
	if !strings.Contains(err.Error(), "layout template") {
		t.Fatalf("error should mention 'layout template', got: %v", err)
	}
}

func TestNewBrokenLayout(t *testing.T) {
	m := stubAssetsWithout()
	m["templates/layout.html"] = &fstest.MapFile{Data: []byte(`{{define "layout.html"}}{{ end `)}
	_, err := New(Deps{
		Cfg:      config.Config{DataDir: t.TempDir(), MediaRoot: t.TempDir()},
		AssetsFS: m,
	})
	if err == nil {
		t.Fatal("New() should return error for broken layout")
	}
	if !strings.Contains(err.Error(), "layout template") {
		t.Fatalf("error should mention 'layout template', got: %v", err)
	}
}

func TestNewMissingPageTemplate(t *testing.T) {
	_, err := New(Deps{
		Cfg:      config.Config{DataDir: t.TempDir(), MediaRoot: t.TempDir()},
		AssetsFS: stubAssetsWithout("home.html"),
	})
	if err == nil {
		t.Fatal("New() should return error for missing page template")
	}
	if !strings.Contains(err.Error(), "home.html") {
		t.Fatalf("error should mention 'home.html', got: %v", err)
	}
}

func TestNewMultipleBrokenPages(t *testing.T) {
	_, err := New(Deps{
		Cfg:      config.Config{DataDir: t.TempDir(), MediaRoot: t.TempDir()},
		AssetsFS: stubAssetsWithout("home.html", "libraries_new.html", "player.html"),
	})
	if err == nil {
		t.Fatal("New() should return error for multiple broken pages")
	}
	errStr := err.Error()
	for _, page := range []string{"home.html", "libraries_new.html", "player.html"} {
		if !strings.Contains(errStr, page) {
			t.Errorf("error should mention '%s', got: %v", page, errStr)
		}
	}
}
