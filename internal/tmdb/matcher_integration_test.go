package tmdb

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	isodb "isomedia/internal/db"
)

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

// smallJPEG is a minimal valid JPEG (2 bytes SOI + 2 bytes EOI).
var smallJPEG = []byte{0xFF, 0xD8, 0xFF, 0xD9}

// newMockTMDBServer creates an httptest.Server that simulates TMDB API responses
// for Breaking Bad (show 1396).
func newMockTMDBServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/3/search/tv", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, sampleSearchJSON)
	})

	mux.HandleFunc("/3/tv/1396/season/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, sampleSeasonJSON)
	})

	mux.HandleFunc("/3/tv/1396", func(w http.ResponseWriter, r *http.Request) {
		// Distinguish show fetch from season fetch by checking remaining path.
		path := r.URL.Path
		if strings.Contains(path, "/season/") {
			// Should not reach here because the more specific pattern above matches first.
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, sampleShowJSON)
	})

	mux.HandleFunc("/t/p/w500/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(smallJPEG)
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// newTestMatcher creates a Matcher wired to the mock server and test DB.
func newTestMatcher(t *testing.T, db *sql.DB, srv *httptest.Server) *Matcher {
	t.Helper()

	// Closed channel returns zero value immediately -- bypasses rate limiter.
	ch := make(chan time.Time)
	close(ch)

	artDir := filepath.Join(t.TempDir(), "thumbs", "tv")

	return &Matcher{
		db: db,
		client: &Client{
			apiKey:     "test-key",
			httpClient: srv.Client(),
			apiBase:    srv.URL + "/3",
			imgBase:    srv.URL + "/t/p/w500",
			limiter:    ch,
		},
		artDir: artDir,
	}
}

// seedTVFile inserts a library, tv_series_files row, tv_series_identities row, and scan_jobs row.
// Returns libraryID, fileID, jobID.
func seedTVFile(t *testing.T, db *sql.DB, title, absPath string, seasonNum int, episodeCSV string) (int64, int64, int64) {
	t.Helper()
	ctx := context.Background()

	// Insert library.
	res, err := db.ExecContext(ctx,
		"INSERT INTO libraries (name, type, root_path) VALUES ('tv-lib', 'tv', '/tv')")
	if err != nil {
		t.Fatalf("insert library: %v", err)
	}
	libID, _ := res.LastInsertId()

	// Insert tv_series_files.
	res, err = db.ExecContext(ctx,
		"INSERT INTO tv_series_files (library_id, abs_path) VALUES (?, ?)",
		libID, absPath)
	if err != nil {
		t.Fatalf("insert tv_series_files: %v", err)
	}
	fileID, _ := res.LastInsertId()

	// Insert tv_series_identities.
	_, err = db.ExecContext(ctx,
		`INSERT INTO tv_series_identities (file_id, status, guessed_title, season_number, episode_numbers_csv)
		 VALUES (?, 'unmatched', ?, ?, ?)`,
		fileID, title, seasonNum, episodeCSV)
	if err != nil {
		t.Fatalf("insert tv_series_identities: %v", err)
	}

	// Insert scan_jobs.
	res, err = db.ExecContext(ctx,
		"INSERT INTO scan_jobs (library_id, job_type, status) VALUES (?, 'tv_match', 'running')",
		libID)
	if err != nil {
		t.Fatalf("insert scan_jobs: %v", err)
	}
	jobID, _ := res.LastInsertId()

	return libID, fileID, jobID
}

func TestRunTVMatchIntegrationHappyPath(t *testing.T) {
	db := openTestDB(t)
	srv := newMockTMDBServer(t)
	m := newTestMatcher(t, db, srv)
	ctx := context.Background()

	libID, fileID, jobID := seedTVFile(t, db, "Breaking Bad", "/tv/breaking.bad.s01e01.mp4", 1, "1")

	if err := m.RunTVMatch(ctx, jobID, libID); err != nil {
		t.Fatalf("RunTVMatch: %v", err)
	}

	t.Run("identity_matched", func(t *testing.T) {
		var status, provider, seriesID string
		var confidence float64
		err := db.QueryRowContext(ctx,
			"SELECT status, provider, series_id, match_confidence FROM tv_series_identities WHERE file_id=?",
			fileID).Scan(&status, &provider, &seriesID, &confidence)
		if err != nil {
			t.Fatalf("query identity: %v", err)
		}
		if status != "matched" {
			t.Fatalf("status = %q, want matched", status)
		}
		if provider != "tmdb" {
			t.Fatalf("provider = %q, want tmdb", provider)
		}
		if seriesID != "1396" {
			t.Fatalf("series_id = %q, want 1396", seriesID)
		}
		if confidence <= 0 {
			t.Fatalf("match_confidence = %v, want > 0", confidence)
		}
	})

	t.Run("metadata_cache", func(t *testing.T) {
		// Show cache.
		var payload string
		err := db.QueryRowContext(ctx,
			"SELECT payload_json FROM tv_series_metadata_cache WHERE entity_key='show:1396'").Scan(&payload)
		if err != nil {
			t.Fatalf("show cache: %v", err)
		}
		if payload == "" || payload == "{}" {
			t.Fatalf("show cache payload empty")
		}

		// Season cache.
		err = db.QueryRowContext(ctx,
			"SELECT payload_json FROM tv_series_metadata_cache WHERE entity_key='show:1396:season:1'").Scan(&payload)
		if err != nil {
			t.Fatalf("season cache: %v", err)
		}
		if payload == "" || payload == "{}" {
			t.Fatalf("season cache payload empty")
		}

		// Episode caches.
		var epCount int
		err = db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM tv_series_metadata_cache WHERE entity_key LIKE 'show:1396:season:1:episode:%'").Scan(&epCount)
		if err != nil {
			t.Fatalf("episode cache count: %v", err)
		}
		if epCount != 2 {
			t.Fatalf("episode cache count = %d, want 2", epCount)
		}
	})

	t.Run("art_downloads", func(t *testing.T) {
		// Series poster.
		var artPath string
		err := db.QueryRowContext(ctx,
			"SELECT art_path FROM tv_series_art WHERE tmdb_series_id=1396 AND art_type='series_poster'").Scan(&artPath)
		if err != nil {
			t.Fatalf("series_poster row: %v", err)
		}
		if _, err := os.Stat(artPath); err != nil {
			t.Fatalf("series_poster file missing: %v", err)
		}

		// Series backdrop.
		err = db.QueryRowContext(ctx,
			"SELECT art_path FROM tv_series_art WHERE tmdb_series_id=1396 AND art_type='series_backdrop'").Scan(&artPath)
		if err != nil {
			t.Fatalf("series_backdrop row: %v", err)
		}
		if _, err := os.Stat(artPath); err != nil {
			t.Fatalf("series_backdrop file missing: %v", err)
		}

		// Season poster.
		err = db.QueryRowContext(ctx,
			"SELECT art_path FROM tv_series_art WHERE tmdb_series_id=1396 AND art_type='season_poster' AND season_number=1").Scan(&artPath)
		if err != nil {
			t.Fatalf("season_poster row: %v", err)
		}
		if _, err := os.Stat(artPath); err != nil {
			t.Fatalf("season_poster file missing: %v", err)
		}
	})

	t.Run("job_progress", func(t *testing.T) {
		var current, total int
		err := db.QueryRowContext(ctx,
			"SELECT progress_current, progress_total FROM scan_jobs WHERE id=?", jobID).Scan(&current, &total)
		if err != nil {
			t.Fatalf("job progress: %v", err)
		}
		if total != 1 {
			t.Fatalf("progress_total = %d, want 1", total)
		}
		if current != 1 {
			t.Fatalf("progress_current = %d, want 1", current)
		}
	})
}

func TestRunTVMatchIntegrationPartialFailure(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// Mock server: search works for both, but show fetch fails for 1396, succeeds for 999.
	mux := http.NewServeMux()

	mux.HandleFunc("/3/search/tv", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("query")
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(strings.ToLower(query), "wire") {
			fmt.Fprint(w, `{
				"page":1,
				"results":[{
					"id":999,
					"name":"The Wire",
					"first_air_date":"2002-06-02",
					"overview":"Drug trade in Baltimore.",
					"poster_path":"/wire_poster.jpg",
					"popularity":180.0
				}],
				"total_results":1
			}`)
		} else {
			// Breaking Bad search -- returns show 1396.
			fmt.Fprint(w, sampleSearchJSON)
		}
	})

	// Show 1396 fetch fails with 500.
	mux.HandleFunc("/3/tv/1396", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	// Show 999 fetch succeeds.
	wireShowJSON := `{
		"id":999,
		"name":"The Wire",
		"overview":"Drug trade in Baltimore.",
		"first_air_date":"2002-06-02",
		"poster_path":"/wire_poster.jpg",
		"backdrop_path":"/wire_backdrop.jpg",
		"status":"Ended",
		"genres":[{"id":18,"name":"Drama"}],
		"seasons":[{"season_number":1,"name":"Season 1","poster_path":"/wire_s1.jpg","air_date":"2002-06-02"}]
	}`
	wireSeasonJSON := `{
		"season_number":1,
		"name":"Season 1",
		"overview":"First season.",
		"poster_path":"/wire_s1.jpg",
		"air_date":"2002-06-02",
		"episodes":[
			{"episode_number":1,"name":"The Target","overview":"Ep 1.","still_path":"","air_date":"2002-06-02","vote_average":8.5}
		]
	}`

	mux.HandleFunc("/3/tv/999/season/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, wireSeasonJSON)
	})

	mux.HandleFunc("/3/tv/999", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/season/") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, wireShowJSON)
	})

	mux.HandleFunc("/t/p/w500/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(smallJPEG)
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// Seed: two files with different titles, same library.
	res, err := db.ExecContext(ctx,
		"INSERT INTO libraries (name, type, root_path) VALUES ('tv-lib', 'tv', '/tv')")
	if err != nil {
		t.Fatalf("insert library: %v", err)
	}
	libID, _ := res.LastInsertId()

	// File 1: Breaking Bad (will fail on show fetch).
	res, err = db.ExecContext(ctx,
		"INSERT INTO tv_series_files (library_id, abs_path) VALUES (?, '/tv/breaking.bad.s01e01.mp4')", libID)
	if err != nil {
		t.Fatalf("insert file 1: %v", err)
	}
	fileID1, _ := res.LastInsertId()
	_, err = db.ExecContext(ctx,
		`INSERT INTO tv_series_identities (file_id, status, guessed_title, season_number, episode_numbers_csv)
		 VALUES (?, 'unmatched', 'Breaking Bad', 1, '1')`, fileID1)
	if err != nil {
		t.Fatalf("insert identity 1: %v", err)
	}

	// File 2: The Wire (will succeed).
	res, err = db.ExecContext(ctx,
		"INSERT INTO tv_series_files (library_id, abs_path) VALUES (?, '/tv/the.wire.s01e01.mp4')", libID)
	if err != nil {
		t.Fatalf("insert file 2: %v", err)
	}
	fileID2, _ := res.LastInsertId()
	_, err = db.ExecContext(ctx,
		`INSERT INTO tv_series_identities (file_id, status, guessed_title, season_number, episode_numbers_csv)
		 VALUES (?, 'unmatched', 'The Wire', 1, '1')`, fileID2)
	if err != nil {
		t.Fatalf("insert identity 2: %v", err)
	}

	// Insert job.
	res, err = db.ExecContext(ctx,
		"INSERT INTO scan_jobs (library_id, job_type, status) VALUES (?, 'tv_match', 'running')", libID)
	if err != nil {
		t.Fatalf("insert job: %v", err)
	}
	jobID, _ := res.LastInsertId()

	ch := make(chan time.Time)
	close(ch)
	m := &Matcher{
		db: db,
		client: &Client{
			apiKey:     "test-key",
			httpClient: srv.Client(),
			apiBase:    srv.URL + "/3",
			imgBase:    srv.URL + "/t/p/w500",
			limiter:    ch,
		},
		artDir: filepath.Join(t.TempDir(), "thumbs", "tv"),
	}

	// RunTVMatch should NOT return an error even though one show fetch fails.
	if err := m.RunTVMatch(ctx, jobID, libID); err != nil {
		t.Fatalf("RunTVMatch should not fail: %v", err)
	}

	t.Run("failed_group_stays_unmatched", func(t *testing.T) {
		var status string
		err := db.QueryRowContext(ctx,
			"SELECT status FROM tv_series_identities WHERE file_id=?", fileID1).Scan(&status)
		if err != nil {
			t.Fatalf("query file 1 identity: %v", err)
		}
		if status != "unmatched" {
			t.Fatalf("file 1 status = %q, want unmatched", status)
		}
	})

	t.Run("successful_group_matched", func(t *testing.T) {
		var status, provider, seriesID string
		err := db.QueryRowContext(ctx,
			"SELECT status, provider, series_id FROM tv_series_identities WHERE file_id=?", fileID2).Scan(&status, &provider, &seriesID)
		if err != nil {
			t.Fatalf("query file 2 identity: %v", err)
		}
		if status != "matched" {
			t.Fatalf("file 2 status = %q, want matched", status)
		}
		if provider != "tmdb" {
			t.Fatalf("file 2 provider = %q, want tmdb", provider)
		}
		if seriesID != "999" {
			t.Fatalf("file 2 series_id = %q, want 999", seriesID)
		}
	})
}
