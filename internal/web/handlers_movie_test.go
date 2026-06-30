package web

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hespera/internal/config"
)

// seedMovieLibrary ensures a movies library exists and returns its id.
func seedMovieLibrary(t *testing.T, db *sql.DB, mediaRoot string) int64 {
	t.Helper()
	var libID int64
	if err := db.QueryRow("SELECT id FROM libraries WHERE type='movies' LIMIT 1").Scan(&libID); err == nil {
		return libID
	}
	res, err := db.Exec(
		"INSERT INTO libraries (name, type, root_path) VALUES ('Movie Test', 'movies', ?)",
		mediaRoot,
	)
	if err != nil {
		t.Fatalf("seedMovieLibrary: %v", err)
	}
	libID, _ = res.LastInsertId()
	return libID
}

// seedMovieFile inserts a movie_files row with the given guessed_title/year/status
// (and tmdb_id when matched). Returns the file id.
func seedMovieFile(t *testing.T, db *sql.DB, guessedTitle string, year int, status string, tmdbID int, mediaRoot string) int64 {
	t.Helper()
	libID := seedMovieLibrary(t, db, mediaRoot)
	absPath := filepath.Join(mediaRoot, fmt.Sprintf("%s (%d).mkv", guessedTitle, year))
	res, err := db.Exec(
		`INSERT INTO movie_files (library_id, abs_path, guessed_title, year, tmdb_id, match_status)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		libID, absPath, guessedTitle, year, tmdbID, status,
	)
	if err != nil {
		t.Fatalf("seedMovieFile: %v", err)
	}
	fileID, _ := res.LastInsertId()
	return fileID
}

// seedMovieMetadata inserts a movie_metadata_cache row so movieDetail/moviePlayer
// can find a film for the given tmdbID.
func seedMovieMetadata(t *testing.T, db *sql.DB, tmdbID int) {
	t.Helper()
	payload := `{"title":"Test Movie","release_date":"2024-05-01","overview":"A test film","genres":[],"runtime":100}`
	if _, err := db.Exec(
		"INSERT INTO movie_metadata_cache (entity_key, lang, payload_json) VALUES (?, 'en', ?)",
		fmt.Sprintf("movie:%d", tmdbID), payload,
	); err != nil {
		t.Fatalf("seedMovieMetadata: %v", err)
	}
}

func TestMovieMatchReviewHandlers(t *testing.T) {
	h, db := newTestHandler(t)
	router := h.Router()

	t.Run("GET_review_200_with_unmatched", func(t *testing.T) {
		seedMovieFile(t, db, "Test Film", 2024, "unmatched", 0, h.cfg.MediaRoot)

		req := httptest.NewRequest(http.MethodGet, "/movies/match/review", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "Test Film") {
			t.Fatalf("response body should contain 'Test Film', got: %s", rec.Body.String())
		}
	})

	t.Run("GET_review_200_empty", func(t *testing.T) {
		if _, err := db.Exec("DELETE FROM movie_files WHERE match_status IN ('', 'unmatched')"); err != nil {
			t.Fatalf("cleanup: %v", err)
		}
		req := httptest.NewRequest(http.MethodGet, "/movies/match/review", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "No movies need review") {
			t.Fatalf("body should contain 'No movies need review', got: %s", rec.Body.String())
		}
	})

	t.Run("POST_review_405", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/movies/match/review", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("expected 405, got %d", rec.Code)
		}
	})

	t.Run("POST_approve_marks_matched_and_enqueues", func(t *testing.T) {
		// A separate handler with a TMDB key set — approve is async, so the handler
		// returns 303 immediately and the metadata fetch runs as a background job
		// (which fails on the fake key, but the DB row is updated synchronously).
		dir := t.TempDir()
		setupTemplateDir(t, dir)
		withChdir(t, dir)
		approveDB := openTestDB(t)
		mediaRoot := filepath.Join(dir, "media")
		if err := os.MkdirAll(mediaRoot, 0o755); err != nil {
			t.Fatalf("MkdirAll media: %v", err)
		}
		ah, err := New(Deps{
			Cfg: config.Config{DataDir: dir, MediaRoot: mediaRoot, TMDBAPIKey: "test-key"},
			DB:  approveDB,
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		arouter := ah.Router()

		seedMovieFile(t, approveDB, "Approve Me", 2020, "unmatched", 0, mediaRoot)

		body := strings.NewReader("guessed_title=Approve+Me&year=2020&tmdb_id=603")
		req := httptest.NewRequest(http.MethodPost, "/movies/match/approve", body)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		arouter.ServeHTTP(rec, req)

		if rec.Code != http.StatusSeeOther {
			t.Fatalf("expected 303, got %d; body: %s", rec.Code, rec.Body.String())
		}
		var status, source string
		var tmdbID int
		if err := approveDB.QueryRow(
			"SELECT match_status, match_source, tmdb_id FROM movie_files WHERE guessed_title='Approve Me'",
		).Scan(&status, &source, &tmdbID); err != nil {
			t.Fatalf("query movie_files: %v", err)
		}
		if status != "matched" || source != "manual" || tmdbID != 603 {
			t.Fatalf("expected matched/manual/603, got %s/%s/%d", status, source, tmdbID)
		}
		// The enqueued movie_metadata_fetch writes into DataDir; let it terminate
		// before TempDir cleanup (it fails on the fake key — terminal either way).
		waitForJobTerminal(t, approveDB, "movie_metadata_fetch")
	})

	t.Run("POST_approve_missing_params", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/movies/match/approve", nil)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d", rec.Code)
		}
	})

	t.Run("POST_approve_no_key", func(t *testing.T) {
		// The default test handler has no TMDB key → 400 even with valid params.
		body := strings.NewReader("guessed_title=Nokey&year=2020&tmdb_id=603")
		req := httptest.NewRequest(http.MethodPost, "/movies/match/approve", body)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 (no key), got %d", rec.Code)
		}
	})

	t.Run("POST_skip_updates_status", func(t *testing.T) {
		seedMovieFile(t, db, "Skip Me", 2019, "unmatched", 0, h.cfg.MediaRoot)

		body := strings.NewReader("guessed_title=Skip+Me&year=2019")
		req := httptest.NewRequest(http.MethodPost, "/movies/match/skip", body)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("expected 303, got %d", rec.Code)
		}
		var status string
		if err := db.QueryRow(
			"SELECT match_status FROM movie_files WHERE guessed_title='Skip Me'",
		).Scan(&status); err != nil {
			t.Fatalf("query: %v", err)
		}
		if status != "skipped" {
			t.Fatalf("expected 'skipped', got '%s'", status)
		}
	})

	t.Run("POST_skip_missing_title", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/movies/match/skip", nil)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d", rec.Code)
		}
	})

	t.Run("POST_unmatch_resets_and_drops_art", func(t *testing.T) {
		seedMovieFile(t, db, "Unmatch Me", 2021, "matched", 4242, h.cfg.MediaRoot)
		if _, err := db.Exec(
			"INSERT INTO movie_art (tmdb_movie_id, art_type, art_path) VALUES (4242,'poster','/x.jpg')",
		); err != nil {
			t.Fatalf("seed art: %v", err)
		}

		body := strings.NewReader("tmdb_id=4242")
		req := httptest.NewRequest(http.MethodPost, "/movie/unmatch", body)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("expected 303, got %d", rec.Code)
		}

		var status string
		var tmdbID int
		if err := db.QueryRow(
			"SELECT match_status, tmdb_id FROM movie_files WHERE guessed_title='Unmatch Me'",
		).Scan(&status, &tmdbID); err != nil {
			t.Fatalf("query: %v", err)
		}
		if status != "" || tmdbID != 0 {
			t.Fatalf("expected unmatched (''/0), got '%s'/%d", status, tmdbID)
		}
		var artCount int
		_ = db.QueryRow("SELECT COUNT(*) FROM movie_art WHERE tmdb_movie_id=4242").Scan(&artCount)
		if artCount != 0 {
			t.Fatalf("expected movie_art dropped, got %d rows", artCount)
		}
	})

	t.Run("POST_unmatch_bad_id", func(t *testing.T) {
		body := strings.NewReader("tmdb_id=0")
		req := httptest.NewRequest(http.MethodPost, "/movie/unmatch", body)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d", rec.Code)
		}
	})

	t.Run("GET_unmatch_405", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/movie/unmatch", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("expected 405, got %d", rec.Code)
		}
	})
}

func TestMovieDetailAndArt(t *testing.T) {
	h, db := newTestHandler(t)
	router := h.Router()

	t.Run("detail_404_unknown", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/movie/999999", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("expected 404, got %d", rec.Code)
		}
	})

	t.Run("detail_200_with_metadata", func(t *testing.T) {
		seedMovieMetadata(t, db, 555)
		seedMovieFile(t, db, "Test Movie", 2024, "matched", 555, h.cfg.MediaRoot)
		req := httptest.NewRequest(http.MethodGet, "/movie/555", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("detail_200_metadata_missing_but_file_matched", func(t *testing.T) {
		// A just-approved film whose metadata fetch hasn't finished: matched file,
		// no movie_metadata_cache row → graceful render (200), not 404.
		seedMovieFile(t, db, "Pending Meta", 2023, "matched", 888, h.cfg.MediaRoot)
		req := httptest.NewRequest(http.MethodGet, "/movie/888", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 (graceful fallback), got %d; body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("art_404_bad_type", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/art/movie/banner/555", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("expected 404, got %d", rec.Code)
		}
	})

	t.Run("art_poster_fallback_redirect", func(t *testing.T) {
		// No movie_art row → poster falls back to the static placeholder (302).
		req := httptest.NewRequest(http.MethodGet, "/art/movie/poster/424242", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusFound {
			t.Fatalf("expected 302, got %d", rec.Code)
		}
	})
}

func TestMovieCastBackfillEnqueue(t *testing.T) {
	// movieDetail enqueues a movie_cast_fetch job once when the movie:%d:cast
	// marker is absent and a TMDB key is set, and not when the marker exists.
	dir := t.TempDir()
	setupTemplateDir(t, dir)
	withChdir(t, dir)
	db := openTestDB(t)
	mediaRoot := filepath.Join(dir, "media")
	if err := os.MkdirAll(mediaRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll media: %v", err)
	}
	h, err := New(Deps{
		Cfg: config.Config{DataDir: dir, MediaRoot: mediaRoot, TMDBAPIKey: "test-key"},
		DB:  db,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	router := h.Router()

	seedMovieMetadata(t, db, 777)
	seedMovieFile(t, db, "Cast Backfill", 2024, "matched", 777, mediaRoot)

	req := httptest.NewRequest(http.MethodGet, "/movie/777", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	// A movie_cast_fetch job was enqueued (it fails on the fake key — terminal).
	waitForJobTerminal(t, db, "movie_cast_fetch")

	// With the marker present, a second view must NOT enqueue another job.
	if _, err := db.Exec(
		"INSERT INTO movie_metadata_cache (entity_key, payload_json) VALUES (?, '{}')",
		fmt.Sprintf("movie:%d:cast", 777),
	); err != nil {
		t.Fatalf("seed cast marker: %v", err)
	}
	var before int
	_ = db.QueryRow("SELECT COUNT(*) FROM scan_jobs WHERE job_type='movie_cast_fetch'").Scan(&before)

	req2 := httptest.NewRequest(http.MethodGet, "/movie/777", nil)
	rec2 := httptest.NewRecorder()
	router.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("second view: expected 200, got %d", rec2.Code)
	}
	var after int
	_ = db.QueryRow("SELECT COUNT(*) FROM scan_jobs WHERE job_type='movie_cast_fetch'").Scan(&after)
	if after != before {
		t.Fatalf("marker present should suppress enqueue: job count went %d → %d", before, after)
	}
}
