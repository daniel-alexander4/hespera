package web

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hespera/internal/config"
	isodb "hespera/internal/db"
)

// seedJourney builds a small library + a ready 1968 journey with three items:
//   - an album owned via release-group MBID (White Album, Nov),
//   - an unowned album (Ghost, Mar),
//   - a single owned via normalized title+artist (Hey Jude, Aug).
//
// Returns the library id.
func seedJourney(t *testing.T, db *sql.DB) int64 {
	t.Helper()

	res, err := db.Exec(`INSERT INTO libraries (name, type, root_path) VALUES ('M','music','/m')`)
	if err != nil {
		t.Fatalf("library: %v", err)
	}
	libID, _ := res.LastInsertId()

	res, err = db.Exec(`INSERT INTO music_artists (library_id, name, musicbrainz_id) VALUES (?, 'The Beatles', 'mbid-beatles')`, libID)
	if err != nil {
		t.Fatalf("artist: %v", err)
	}
	artistID, _ := res.LastInsertId()

	// Owned album, matched to the journey item by its release-group MBID.
	res, err = db.Exec(`INSERT INTO music_albums (library_id, artist_id, album_artist_id, title, year, musicbrainz_id, is_compilation) VALUES (?,?,?,?,1968,'rg-white',0)`,
		libID, artistID, artistID, "The Beatles")
	if err != nil {
		t.Fatalf("album white: %v", err)
	}
	whiteID, _ := res.LastInsertId()
	if _, err := db.Exec(`INSERT INTO music_tracks (library_id, artist_id, album_id, title, track_no, disc_no, abs_path, mime_type) VALUES (?,?,?,?,1,1,'/m/ussr.mp3','audio/mpeg')`,
		libID, artistID, whiteID, "Back in the U.S.S.R."); err != nil {
		t.Fatalf("track ussr: %v", err)
	}

	// A second album holding the standalone single track (no MBID — reconciled by
	// normalized title+artist).
	res, err = db.Exec(`INSERT INTO music_albums (library_id, artist_id, album_artist_id, title, year, is_compilation) VALUES (?,?,?,?,1968,0)`,
		libID, artistID, artistID, "Singles")
	if err != nil {
		t.Fatalf("album singles: %v", err)
	}
	singlesID, _ := res.LastInsertId()
	if _, err := db.Exec(`INSERT INTO music_tracks (library_id, artist_id, album_id, title, track_no, disc_no, abs_path, mime_type) VALUES (?,?,?,?,1,1,'/m/heyjude.mp3','audio/mpeg')`,
		libID, artistID, singlesID, "Hey Jude"); err != nil {
		t.Fatalf("track hey jude: %v", err)
	}

	if _, err := db.Exec(`INSERT INTO year_journeys (year, status) VALUES (1968, 'ready')`); err != nil {
		t.Fatalf("journey: %v", err)
	}
	items := []struct {
		kind, artist, title, rg, date string
		peak                          int
	}{
		{"album", "The Beatles", "The Beatles", "rg-white", "1968-11-22", 1}, // owned via MBID
		{"album", "Nobody At All", "Ghost", "rg-ghost", "1968-03-01", 40},    // unowned
		{"single", "The Beatles", "Hey Jude", "", "1968-08-30", 1},           // owned via title+artist
	}
	for _, it := range items {
		if _, err := db.Exec(`INSERT INTO year_journey_items (year, kind, artist_name, title, rg_mbid, release_date, chart_peak) VALUES (1968,?,?,?,?,?,?)`,
			it.kind, it.artist, it.title, it.rg, it.date, it.peak); err != nil {
			t.Fatalf("item %s: %v", it.title, err)
		}
	}
	return libID
}

func TestMusicYearReconcileAndOrder(t *testing.T) {
	h, db := newTestHandler(t)
	seedJourney(t, db)
	router := h.Router()

	req := httptest.NewRequest(http.MethodGet, "/music/year?y=1968", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /music/year = %d", rec.Code)
	}
	body := rec.Body.String()

	// 2 of 3 acquired (White Album via MBID, Hey Jude via title+artist).
	if !strings.Contains(body, `id="counts">2/3<`) {
		t.Fatalf("counts not 2/3: %s", body)
	}
	// Owned → Play control present.
	if !strings.Contains(body, `href="/music/player?source=journey&y=1968"`) {
		t.Fatalf("play control missing: %s", body)
	}
	// Chronological order: Ghost (Mar) → Hey Jude (Aug) → The Beatles (Nov).
	gi := strings.Index(body, "Ghost")
	hj := strings.Index(body, "Hey Jude")
	wa := strings.Index(body, ">The Beatles<")
	if gi < 0 || hj < 0 || wa < 0 || !(gi < hj && hj < wa) {
		t.Fatalf("items out of chronological order (ghost=%d heyjude=%d white=%d): %s", gi, hj, wa, body)
	}
}

func TestJourneyQueueOwnedOnly(t *testing.T) {
	h, db := newTestHandler(t)
	seedJourney(t, db)
	router := h.Router()

	req := httptest.NewRequest(http.MethodGet, "/music/queue?source=journey&y=1968", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /music/queue?source=journey = %d", rec.Code)
	}
	var out struct {
		Title  string `json:"title"`
		Tracks []struct {
			Title string `json:"title"`
		} `json:"tracks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode queue: %v (%s)", err, rec.Body.String())
	}
	if out.Title != "Rediscover 1968" {
		t.Fatalf("queue title = %q", out.Title)
	}
	// Only the two owned items contribute tracks, in chronological order:
	// the Aug single (Hey Jude) before the Nov album (Back in the U.S.S.R.).
	if len(out.Tracks) != 2 {
		t.Fatalf("queue has %d tracks, want 2 (owned only): %+v", len(out.Tracks), out.Tracks)
	}
	if out.Tracks[0].Title != "Hey Jude" || out.Tracks[1].Title != "Back in the U.S.S.R." {
		t.Fatalf("queue order wrong: %+v", out.Tracks)
	}
}

// TestMusicYearRealTemplate renders the page through the REAL web/templates so a
// field/method typo in music_year.html (which the stub-template tests can't
// catch) fails here. It compiles every real template via New() and executes the
// year page against seeded data.
func TestMusicYearRealTemplate(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	root := filepath.Join(wd, "..", "..") // internal/web → repo root (has web/templates)
	withChdir(t, root)

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.sqlite")
	db, err := isodb.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := isodb.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	h, err := New(Deps{Cfg: config.Config{DataDir: dir, MediaRoot: dir}, DB: db})
	if err != nil {
		t.Fatalf("New with real templates: %v", err)
	}
	seedJourney(t, db)

	req := httptest.NewRequest(http.MethodGet, "/music/year?y=1968", nil)
	rec := httptest.NewRecorder()
	h.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /music/year = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"Rediscover 1968", "Charted songs", "Play 1968"} {
		if !strings.Contains(body, want) {
			t.Fatalf("real template missing %q", want)
		}
	}
}
