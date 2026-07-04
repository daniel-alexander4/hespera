package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"hespera/internal/config"
)

// TestEraPickerRendersPlayShuffle uses the real embedded templates (no AssetsFS)
// to verify the shared era-picker partial renders on both the music-home and the
// Home Quick-Play card, with Play + Shuffle buttons and no keyboard-hint line.
func TestEraPickerRendersPlayShuffle(t *testing.T) {
	db := openTestDB(t)
	h, err := New(Deps{
		Cfg: config.Config{DataDir: t.TempDir(), MediaRoot: t.TempDir()},
		DB:  db,
	})
	if err != nil {
		t.Fatalf("New (embedded templates): %v", err)
	}

	// A music library with one year-tagged album → eraPicker returns non-nil.
	res, err := db.Exec("INSERT INTO libraries (name, type, root_path) VALUES ('M','music','/m')")
	if err != nil {
		t.Fatalf("insert lib: %v", err)
	}
	libID, _ := res.LastInsertId()
	ares, err := db.Exec("INSERT INTO music_artists (library_id, name) VALUES (?, 'A')", libID)
	if err != nil {
		t.Fatalf("insert artist: %v", err)
	}
	artistID, _ := ares.LastInsertId()
	if _, err := db.Exec(
		"INSERT INTO music_albums (library_id, artist_id, album_artist_id, title, year) VALUES (?, ?, ?, 'Alb', 1985)",
		libID, artistID, artistID); err != nil {
		t.Fatalf("insert album: %v", err)
	}

	// Garbage year tags (real library data: a 2-digit "98", a doubled "20132013")
	// must not stretch the picker: era_slider.js draws one tick per year of the
	// span, so an unclamped span DOM-bombs the browser.
	for _, garbage := range []int{98, 1079, 20132013} {
		if _, err := db.Exec(
			"INSERT INTO music_albums (library_id, artist_id, album_artist_id, title, year) VALUES (?, ?, ?, 'G', ?)",
			libID, artistID, artistID, garbage); err != nil {
			t.Fatalf("insert garbage-year album: %v", err)
		}
	}

	router := h.Router()
	for _, path := range []string{"/", "/music"} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d", path, rec.Code)
		}
		body := rec.Body.String()
		for _, want := range []string{"era-picker", "era-play", "era-shuffle", `data-min="1985"`, `data-max="1985"`} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: expected %q in page", path, want)
			}
		}
		for _, gone := range []string{"era-hint", "Shuffle Era", "Enter to shuffle", `data-max="20132013"`} {
			if strings.Contains(body, gone) {
				t.Errorf("%s: %q should have been removed", path, gone)
			}
		}
	}

	// A library whose only year tags are garbage has no plausible era span —
	// the picker must not render at all.
	if _, err := db.Exec("UPDATE music_albums SET year=20132013 WHERE year=1985"); err != nil {
		t.Fatalf("poison sane album: %v", err)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/music", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/music (all-garbage): status %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "era-picker") {
		t.Error("/music: era-picker should not render when every year tag is implausible")
	}
}
