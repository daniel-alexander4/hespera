package web

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

// setupTemplateDir creates a minimal template directory structure
// with a valid layout and all required page templates.
func setupTemplateDir(t *testing.T, dir string) {
	t.Helper()
	tplDir := filepath.Join(dir, "web", "templates")
	if err := os.MkdirAll(tplDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	// Also create web/static so staticv func doesn't break
	staticDir := filepath.Join(dir, "web", "static")
	if err := os.MkdirAll(staticDir, 0o755); err != nil {
		t.Fatalf("MkdirAll static: %v", err)
	}

	layout := `{{define "layout.html"}}<!DOCTYPE html><html><body>{{template "content" .}}</body></html>{{end}}`
	if err := os.WriteFile(filepath.Join(tplDir, "layout.html"), []byte(layout), 0o644); err != nil {
		t.Fatalf("WriteFile layout: %v", err)
	}

	pages := []string{
		"home.html", "login.html", "libraries.html", "libraries_new.html",
		"settings.html", "settings_jobs.html", "music_home.html", "music_artist.html",
		"music_artist_external.html", "music_artist_disambiguate.html", "music_artist_art.html",
		"music_album.html", "music_albums.html", "music_compilations.html", "music_year.html", "player.html",
		"music_match_review.html", "music_album_edit.html", "music_track_edit.html", "music_duplicates.html",
		"settings_tags.html", "settings_apikeys.html", "settings_about.html", "tv_home.html", "tv_series.html",
		"tv_season.html", "tv_match_review.html", "tv_player.html", "person.html",
		"movies_home.html", "movie_detail.html", "movie_match_review.html", "movie_player.html",
	}
	pageContent := `{{define "content"}}hello{{end}}`
	for _, p := range pages {
		if err := os.WriteFile(filepath.Join(tplDir, p), []byte(pageContent), 0o644); err != nil {
			t.Fatalf("WriteFile %s: %v", p, err)
		}
	}

	// Overwrite music_match_review.html with a functional stub that renders
	// the same data structure the handler passes (.Albums, .LibraryID).
	reviewTpl := `{{define "content"}}` +
		`{{if .Albums}}` +
		`{{if .LibraryID}}<button>Run Match</button>{{end}}` +
		`{{range .Albums}}<span>{{.Title}}</span>{{end}}` +
		`{{else}}<p>No albums need review</p>{{end}}` +
		`{{end}}`
	if err := os.WriteFile(filepath.Join(tplDir, "music_match_review.html"), []byte(reviewTpl), 0o644); err != nil {
		t.Fatalf("WriteFile music_match_review.html override: %v", err)
	}

	// Overwrite tv_match_review.html with a functional stub that renders
	// the Groups data the handler passes.
	tvReviewTpl := `{{define "content"}}` +
		`{{if .Groups}}{{range .Groups}}<span>{{.GuessedTitle}}</span>{{end}}` +
		`{{else}}<p>No series need review</p>{{end}}` +
		`{{end}}`
	if err := os.WriteFile(filepath.Join(tplDir, "tv_match_review.html"), []byte(tvReviewTpl), 0o644); err != nil {
		t.Fatalf("WriteFile tv_match_review.html override: %v", err)
	}

	// Overwrite movie_match_review.html with a functional stub rendering the
	// Groups the handler passes (mirrors the tv_match_review override).
	movieReviewTpl := `{{define "content"}}` +
		`{{if .Groups}}{{range .Groups}}<span>{{.GuessedTitle}}</span>{{end}}` +
		`{{else}}<p>No movies need review</p>{{end}}` +
		`{{end}}`
	if err := os.WriteFile(filepath.Join(tplDir, "movie_match_review.html"), []byte(movieReviewTpl), 0o644); err != nil {
		t.Fatalf("WriteFile movie_match_review.html override: %v", err)
	}

	// Overwrite settings_jobs.html with a functional stub mirroring the real
	// structure: a `content` block that embeds a reusable `jobs-container` block,
	// so the live-poll fragment endpoint (ExecuteTemplate "jobs-container") and
	// the page both render the job rows.
	jobsTpl := `{{define "content"}}<div id="jobs-container">{{template "jobs-container" .}}</div>{{end}}` +
		`{{define "jobs-container"}}{{if .Jobs}}<table>{{range .Jobs}}<tr><td>{{.JobType}}</td>` +
		`<td><span class="badge badge-{{.Status}}">{{.Status}}</span></td></tr>{{end}}</table>` +
		`{{else}}<p>No jobs found.</p>{{end}}{{end}}`
	if err := os.WriteFile(filepath.Join(tplDir, "settings_jobs.html"), []byte(jobsTpl), 0o644); err != nil {
		t.Fatalf("WriteFile settings_jobs.html override: %v", err)
	}

	// Overwrite player.html with a functional stub mirroring the now-playing view:
	// a .player-page shell carrying the autoload query (the queue itself is fetched
	// client-side from /music/queue, so the page no longer injects tracks).
	playerTpl := `{{define "content"}}<div class="player-page" data-autoload="{{.AutoloadQuery}}"></div>{{end}}`
	if err := os.WriteFile(filepath.Join(tplDir, "player.html"), []byte(playerTpl), 0o644); err != nil {
		t.Fatalf("WriteFile player.html override: %v", err)
	}

	// Functional stub for the paginated albums list: one <a class="album"> per row
	// plus the page nav, so a test can assert the LIMIT/OFFSET + pageNav wiring.
	albumsTpl := `{{define "content"}}{{range .Albums}}<a class="album" href="/music/album/{{.ID}}"></a>{{end}}` +
		`<span id="pg">{{.Page.Page}}/{{.Page.TotalPages}}</span><span id="q">{{.Page.Query}}</span>` +
		`{{if .Page.HasPrev}}<a class="prev"></a>{{end}}{{if .Page.HasNext}}<a class="next"></a>{{end}}{{end}}`
	if err := os.WriteFile(filepath.Join(tplDir, "music_albums.html"), []byte(albumsTpl), 0o644); err != nil {
		t.Fatalf("WriteFile music_albums.html override: %v", err)
	}

	// Functional stub for the year-journey page: renders the acquired counts, the
	// Play control (only when something is owned), and one row per item in the
	// handler's chronological order, so a test can assert reconcile + ordering.
	yearTpl := `{{define "content"}}<p id="counts">{{.OwnedSongs}}/{{.TotalSongs}}</p>` +
		`<a id="play" href="/music/player?source=journey&y={{.Year}}{{if .TopFirst}}&dir=top{{end}}" data-play>Play</a>` +
		`<a id="dir" href="/music/year?y={{.Year}}{{if not .TopFirst}}&dir=top{{end}}">dir</a>` +
		`{{range .Weeks}}<section class="wk" data-date="{{.Date}}">{{range .Cards}}<div class="row" data-pos="{{.Pos}}" data-debut="{{.IsDebut}}" data-owned="{{.Owned}}">{{.Title}}</div>{{end}}</section>{{end}}{{end}}`
	if err := os.WriteFile(filepath.Join(tplDir, "music_year.html"), []byte(yearTpl), 0o644); err != nil {
		t.Fatalf("WriteFile music_year.html override: %v", err)
	}
}

// withChdir changes to dir for the duration of the test and restores on cleanup.
func withChdir(t *testing.T, dir string) {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
}

// newTestHandler creates a Handler backed by a real SQLite DB with migrations
// applied and minimal template stubs. It chdir's into a temp dir so template
// compilation finds web/templates/.
func newTestHandler(t *testing.T) (*Handler, *sql.DB) {
	t.Helper()
	dir := t.TempDir()
	setupTemplateDir(t, dir)
	withChdir(t, dir)
	db := openTestDB(t)
	h, err := New(Deps{
		Cfg: config.Config{DataDir: dir, MediaRoot: dir},
		DB:  db,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return h, db
}

func TestNewValidTemplates(t *testing.T) {
	dir := t.TempDir()
	setupTemplateDir(t, dir)
	withChdir(t, dir)

	db := openTestDB(t)
	h, err := New(Deps{
		Cfg: config.Config{DataDir: dir, MediaRoot: dir},
		DB:  db,
	})
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	if h == nil {
		t.Fatal("New() returned nil handler")
	}
	// Verify all page templates are compiled
	expectedPages := 33
	if len(h.tpls) != expectedPages {
		t.Fatalf("expected %d templates, got %d", expectedPages, len(h.tpls))
	}
}

func TestNewMissingLayout(t *testing.T) {
	dir := t.TempDir()
	setupTemplateDir(t, dir)
	// Remove layout file
	os.Remove(filepath.Join(dir, "web", "templates", "layout.html"))
	withChdir(t, dir)

	_, err := New(Deps{
		Cfg: config.Config{DataDir: dir, MediaRoot: dir},
	})
	if err == nil {
		t.Fatal("New() should return error for missing layout")
	}
	if !strings.Contains(err.Error(), "layout template") {
		t.Fatalf("error should mention 'layout template', got: %v", err)
	}
}

func TestNewBrokenLayout(t *testing.T) {
	dir := t.TempDir()
	setupTemplateDir(t, dir)
	// Overwrite layout with invalid template syntax
	layoutPath := filepath.Join(dir, "web", "templates", "layout.html")
	os.WriteFile(layoutPath, []byte(`{{define "layout.html"}}{{ end `), 0o644)
	withChdir(t, dir)

	_, err := New(Deps{
		Cfg: config.Config{DataDir: dir, MediaRoot: dir},
	})
	if err == nil {
		t.Fatal("New() should return error for broken layout")
	}
	if !strings.Contains(err.Error(), "layout template") {
		t.Fatalf("error should mention 'layout template', got: %v", err)
	}
}

func TestNewMissingPageTemplate(t *testing.T) {
	dir := t.TempDir()
	setupTemplateDir(t, dir)
	// Remove one page file
	os.Remove(filepath.Join(dir, "web", "templates", "home.html"))
	withChdir(t, dir)

	_, err := New(Deps{
		Cfg: config.Config{DataDir: dir, MediaRoot: dir},
	})
	if err == nil {
		t.Fatal("New() should return error for missing page template")
	}
	if !strings.Contains(err.Error(), "home.html") {
		t.Fatalf("error should mention 'home.html', got: %v", err)
	}
}

func TestNewMultipleBrokenPages(t *testing.T) {
	dir := t.TempDir()
	setupTemplateDir(t, dir)
	// Remove multiple page files
	os.Remove(filepath.Join(dir, "web", "templates", "home.html"))
	os.Remove(filepath.Join(dir, "web", "templates", "login.html"))
	os.Remove(filepath.Join(dir, "web", "templates", "player.html"))
	withChdir(t, dir)

	_, err := New(Deps{
		Cfg: config.Config{DataDir: dir, MediaRoot: dir},
	})
	if err == nil {
		t.Fatal("New() should return error for multiple broken pages")
	}
	errStr := err.Error()
	for _, page := range []string{"home.html", "login.html", "player.html"} {
		if !strings.Contains(errStr, page) {
			t.Errorf("error should mention '%s', got: %v", page, errStr)
		}
	}
}
