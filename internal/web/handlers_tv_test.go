package web

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"isomedia/internal/config"
)

// waitForJobTerminal blocks until the most recent job of the given type
// reaches a terminal status (done/failed/canceled), so a background job's
// side effects can't outlive the test and race TempDir cleanup.
func waitForJobTerminal(t *testing.T, db *sql.DB, jobType string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		var status string
		err := db.QueryRow(
			"SELECT status FROM scan_jobs WHERE job_type=? ORDER BY id DESC LIMIT 1",
			jobType,
		).Scan(&status)
		if err == nil && (status == "done" || status == "failed" || status == "canceled") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job %q did not reach a terminal state within timeout", jobType)
}

// seedTVMetadata inserts a tv_series_metadata_cache row so tvSeriesDetail
// can find a show entry for the given seriesID.
func seedTVMetadata(t *testing.T, db *sql.DB, seriesID string) {
	t.Helper()
	entityKey := "show:" + seriesID
	payload := `{"name":"Test Show","first_air_date":"2024-01-15","status":"Returning Series","overview":"A test show","poster_path":"/test.jpg","seasons":[],"genres":[]}`
	_, err := db.Exec(
		"INSERT INTO tv_series_metadata_cache (entity_key, lang, payload_json) VALUES (?, 'en', ?)",
		entityKey, payload,
	)
	if err != nil {
		t.Fatalf("seedTVMetadata: %v", err)
	}
}

// seedTVIdentity inserts a library, tv_series_files row, and tv_series_identities
// row with the given guessed_title and status. Returns the file ID.
func seedTVIdentity(t *testing.T, db *sql.DB, guessedTitle, status, mediaRoot string) int64 {
	t.Helper()

	// Ensure a TV library exists.
	var libID int64
	err := db.QueryRow("SELECT id FROM libraries WHERE type='tv' LIMIT 1").Scan(&libID)
	if err != nil {
		res, err := db.Exec(
			"INSERT INTO libraries (name, type, root_path) VALUES ('TV Test', 'tv', ?)",
			mediaRoot,
		)
		if err != nil {
			t.Fatalf("seedTVIdentity: insert library: %v", err)
		}
		libID, _ = res.LastInsertId()
	}

	// Insert a tv_series_files row.
	absPath := filepath.Join(mediaRoot, guessedTitle+".mkv")
	res, err := db.Exec(
		"INSERT INTO tv_series_files (library_id, abs_path, container) VALUES (?, ?, 'mkv')",
		libID, absPath,
	)
	if err != nil {
		t.Fatalf("seedTVIdentity: insert tv_series_files: %v", err)
	}
	fileID, _ := res.LastInsertId()

	// Insert a tv_series_identities row.
	_, err = db.Exec(
		`INSERT INTO tv_series_identities (file_id, status, guessed_title, season_number, episode_numbers_csv)
		 VALUES (?, ?, ?, 1, '1')`,
		fileID, status, guessedTitle,
	)
	if err != nil {
		t.Fatalf("seedTVIdentity: insert tv_series_identities: %v", err)
	}
	return fileID
}

func TestTVMatchReviewHandlers(t *testing.T) {
	h, db := newTestHandler(t)
	router := h.Router()

	t.Run("GET_review_200_with_unmatched", func(t *testing.T) {
		seedTVIdentity(t, db, "Test Show", "unmatched", h.cfg.MediaRoot)

		req := httptest.NewRequest(http.MethodGet, "/tv/match/review", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		if !strings.Contains(body, "Test Show") {
			t.Fatalf("response body should contain 'Test Show', got: %s", body)
		}
	})

	t.Run("GET_review_200_empty", func(t *testing.T) {
		// Clean up any unmatched identities.
		_, err := db.Exec("DELETE FROM tv_series_identities WHERE status='unmatched'")
		if err != nil {
			t.Fatalf("cleanup: %v", err)
		}

		req := httptest.NewRequest(http.MethodGet, "/tv/match/review", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, "No series need review") {
			t.Fatalf("response body should contain 'No series need review', got: %s", body)
		}
	})

	t.Run("POST_review_405", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/tv/match/review", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("expected 405, got %d", rec.Code)
		}
	})

	t.Run("POST_approve_updates_status", func(t *testing.T) {
		// Create a separate handler with TMDBAPIKey set.
		dir := t.TempDir()
		setupTemplateDir(t, dir)
		withChdir(t, dir)
		approveDB := openTestDB(t)

		// Create media root directory for path validation.
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

		seedTVIdentity(t, approveDB, "Approve Me", "unmatched", mediaRoot)

		body := strings.NewReader("guessed_title=Approve+Me&tmdb_id=12345")
		req := httptest.NewRequest(http.MethodPost, "/tv/match/approve", body)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		arouter.ServeHTTP(rec, req)

		if rec.Code != http.StatusSeeOther {
			t.Fatalf("expected 303, got %d; body: %s", rec.Code, rec.Body.String())
		}

		// Verify DB state.
		var status, provider, seriesID string
		err = approveDB.QueryRow(
			"SELECT status, provider, series_id FROM tv_series_identities WHERE guessed_title='Approve Me'",
		).Scan(&status, &provider, &seriesID)
		if err != nil {
			t.Fatalf("query identity: %v", err)
		}
		if status != "matched" {
			t.Fatalf("expected status 'matched', got '%s'", status)
		}
		if provider != "tmdb" {
			t.Fatalf("expected provider 'tmdb', got '%s'", provider)
		}
		if seriesID != "12345" {
			t.Fatalf("expected series_id '12345', got '%s'", seriesID)
		}

		// The approve handler enqueues a background tv_metadata_fetch job that
		// writes into DataDir (the test's TempDir). Wait for it to reach a
		// terminal state before the subtest returns, otherwise its filesystem
		// writes race t.TempDir cleanup.
		waitForJobTerminal(t, approveDB, "tv_metadata_fetch")
	})

	t.Run("POST_approve_missing_params", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/tv/match/approve", nil)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d; body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("POST_skip_updates_status", func(t *testing.T) {
		seedTVIdentity(t, db, "Skip Me", "unmatched", h.cfg.MediaRoot)

		body := strings.NewReader("guessed_title=Skip+Me")
		req := httptest.NewRequest(http.MethodPost, "/tv/match/skip", body)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusSeeOther {
			t.Fatalf("expected 303, got %d; body: %s", rec.Code, rec.Body.String())
		}

		// Verify DB state.
		var status string
		err := db.QueryRow(
			"SELECT status FROM tv_series_identities WHERE guessed_title='Skip Me'",
		).Scan(&status)
		if err != nil {
			t.Fatalf("query identity: %v", err)
		}
		if status != "skipped" {
			t.Fatalf("expected status 'skipped', got '%s'", status)
		}
	})

	t.Run("POST_skip_missing_title", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/tv/match/skip", nil)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d; body: %s", rec.Code, rec.Body.String())
		}
	})
}

func TestTVHandlers(t *testing.T) {
	h, db := newTestHandler(t)
	router := h.Router()

	t.Run("GET /tv 200", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/tv", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
	})

	t.Run("POST /tv 405", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/tv", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("expected 405, got %d", rec.Code)
		}
	})

	t.Run("GET /tv/series/99999 404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/tv/series/99999", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("expected 404, got %d", rec.Code)
		}
		body := rec.Body.String()
		if strings.Contains(strings.ToLower(body), "sql") {
			t.Fatalf("404 body should not contain SQL error text, got: %s", body)
		}
	})

	t.Run("GET /tv/series/12345 200", func(t *testing.T) {
		seedTVMetadata(t, db, "12345")
		req := httptest.NewRequest(http.MethodGet, "/tv/series/12345", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("POST /tv/series/123 405", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/tv/series/123", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("expected 405, got %d", rec.Code)
		}
	})
}
