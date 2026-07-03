package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
)

// Mark-watched endpoints: set/clear completed without playback — single TV
// file, whole TV season, and movie (all matched copies of the tmdb_id).
// Unwatching must reset position so a replay starts fresh. Uses the shared
// postForm helper (handlers_music_unmatch_test.go).

func TestTVMarkWatched(t *testing.T) {
	h, db := newTestHandler(t)
	router := h.Router()

	res, err := db.Exec("INSERT INTO libraries (name, type, root_path) VALUES ('TV', 'tv', ?)", h.cfg.MediaRoot)
	if err != nil {
		t.Fatal(err)
	}
	libID, _ := res.LastInsertId()
	seed := func(ep int) int64 {
		fres, err := db.Exec(
			"INSERT INTO tv_series_files (library_id, abs_path, container) VALUES (?, ?, 'mkv')",
			libID, filepath.Join(h.cfg.MediaRoot, fmt.Sprintf("w.s01e%02d.mkv", ep)))
		if err != nil {
			t.Fatal(err)
		}
		fid, _ := fres.LastInsertId()
		if _, err := db.Exec(
			`INSERT INTO tv_series_identities (file_id, provider, series_id, status, guessed_title, season_number, episode_numbers_csv)
			 VALUES (?, 'tmdb', '600', 'matched', 'Watch Show', 1, ?)`, fid, fmt.Sprint(ep)); err != nil {
			t.Fatal(err)
		}
		return fid
	}
	f1, f2 := seed(1), seed(2)

	completed := func(fid int64) (int, float64) {
		var c int
		var pos float64
		_ = db.QueryRow("SELECT completed, position_seconds FROM tv_playback_progress WHERE file_id=?", fid).Scan(&c, &pos)
		return c, pos
	}

	// Single file → watched.
	rec := postForm(t, router, "/tv/mark-watched", url.Values{"file": {fmt.Sprint(f1)}, "series": {"600"}, "season": {"1"}, "watched": {"1"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d: %s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/tv/season/?series=600&season=1" {
		t.Fatalf("redirect = %q", loc)
	}
	if c, _ := completed(f1); c != 1 {
		t.Fatal("file 1 should be completed")
	}

	// Unwatch resets position: seed a mid-episode position first.
	if _, err := db.Exec("UPDATE tv_playback_progress SET position_seconds=900 WHERE file_id=?", f1); err != nil {
		t.Fatal(err)
	}
	postForm(t, router, "/tv/mark-watched", url.Values{"file": {fmt.Sprint(f1)}, "watched": {"0"}})
	if c, pos := completed(f1); c != 0 || pos != 0 {
		t.Fatalf("unwatch should clear completed + position, got completed=%d pos=%v", c, pos)
	}

	// Whole season bulk.
	postForm(t, router, "/tv/mark-watched", url.Values{"series": {"600"}, "season": {"1"}, "watched": {"1"}})
	if c1, _ := completed(f1); c1 != 1 {
		t.Fatal("season bulk should complete file 1")
	}
	if c2, _ := completed(f2); c2 != 1 {
		t.Fatal("season bulk should complete file 2")
	}

	// Neither file nor series+season → 400; GET → 405.
	if rec := postForm(t, router, "/tv/mark-watched", url.Values{"watched": {"1"}}); rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	req := httptest.NewRequest(http.MethodGet, "/tv/mark-watched", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rr.Code)
	}
}

func TestMovieMarkWatched(t *testing.T) {
	h, db := newTestHandler(t)
	router := h.Router()

	// Two matched copies of one film — both must flip together.
	fa := seedMovieFile(t, db, "Watch Film", 2021, "matched", 9090, h.cfg.MediaRoot)
	res, err := db.Exec(
		`INSERT INTO movie_files (library_id, abs_path, guessed_title, year, tmdb_id, match_status)
		 SELECT library_id, ?, guessed_title, year, tmdb_id, match_status FROM movie_files WHERE id=?`,
		filepath.Join(h.cfg.MediaRoot, "Watch Film (2021) copy.mkv"), fa)
	if err != nil {
		t.Fatal(err)
	}
	fb, _ := res.LastInsertId()

	rec := postForm(t, router, "/movie/mark-watched", url.Values{"tmdb": {"9090"}, "watched": {"1"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/movie/9090" {
		t.Fatalf("got %d → %q", rec.Code, rec.Header().Get("Location"))
	}
	for _, fid := range []int64{fa, fb} {
		var c int
		_ = db.QueryRow("SELECT completed FROM movie_playback_progress WHERE file_id=?", fid).Scan(&c)
		if c != 1 {
			t.Fatalf("file %d should be completed", fid)
		}
	}

	// Unknown tmdb id → 404.
	if rec := postForm(t, router, "/movie/mark-watched", url.Values{"tmdb": {"424242"}, "watched": {"1"}}); rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}
