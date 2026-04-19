package match

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newMockMusicServer creates a single httptest.Server that routes requests by
// URL path to return canned JSON for all external services: MusicBrainz,
// Wikipedia, Wikidata, Wikimedia Commons, and Cover Art Archive.
func newMockMusicServer(t *testing.T) *httptest.Server {
	t.Helper()

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		switch {
		// MusicBrainz artist search
		case path == "/ws/2/artist" && r.URL.RawQuery != "" && strings.Contains(r.URL.RawQuery, "query="):
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"artists": []map[string]interface{}{
					{
						"id":    "test-artist-mbid",
						"name":  "Pink Floyd",
						"score": 100,
					},
				},
			})

		// MusicBrainz artist lookup with URL relations
		case strings.HasPrefix(path, "/ws/2/artist/test-artist-mbid"):
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"id":   "test-artist-mbid",
				"name": "Pink Floyd",
				"relations": []map[string]interface{}{
					{
						"type": "wikipedia",
						"url": map[string]string{
							"resource": srv.URL + "/wiki/Test_Artist",
						},
					},
					{
						"type": "wikidata",
						"url": map[string]string{
							"resource": srv.URL + "/wiki/Q12345",
						},
					},
				},
			})

		// MusicBrainz release-group search (strategy A)
		case path == "/ws/2/release-group" && strings.Contains(r.URL.RawQuery, "query="):
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"release-groups": []map[string]interface{}{
					{
						"id":                 "test-rg-id",
						"title":              "Dark Side of the Moon",
						"primary-type":       "Album",
						"score":              100,
						"first-release-date": "1973-03-01",
						"artist-credit": []map[string]interface{}{
							{
								"name": "Pink Floyd",
								"artist": map[string]string{
									"id":   "test-artist-mbid",
									"name": "Pink Floyd",
								},
							},
						},
						"releases": []map[string]string{
							{"id": "test-release-id", "title": "Dark Side of the Moon"},
						},
					},
				},
			})

		// Wikipedia summary
		case path == "/api/rest_v1/page/summary/Test_Artist":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{
				"extract": "Test artist biography text.",
			})

		// Wikidata entity
		case path == "/wiki/Special:EntityData/Q12345.json":
			w.Header().Set("Content-Type", "application/json")
			entity := map[string]interface{}{
				"entities": map[string]interface{}{
					"Q12345": map[string]interface{}{
						"claims": map[string]interface{}{
							"P18": []map[string]interface{}{
								{
									"mainsnak": map[string]interface{}{
										"datavalue": map[string]interface{}{
											"value": "Test_artist.jpg",
											"type":  "string",
										},
									},
								},
							},
						},
						"sitelinks": map[string]interface{}{
							"enwiki": map[string]string{
								"title": "Test_Artist",
							},
						},
					},
				},
			}
			json.NewEncoder(w).Encode(entity)

		// Wikimedia Commons image download
		case strings.HasPrefix(path, "/wiki/Special:FilePath/"):
			w.Header().Set("Content-Type", "image/jpeg")
			// Minimal valid-ish JPEG bytes (just enough for the test).
			w.Write([]byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00})

		// Cover Art Archive release-group
		case path == "/release-group/test-rg-id":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"images": []map[string]interface{}{
					{
						"types": []string{"Front"},
						"front": true,
						"thumbnails": map[string]string{
							"large": srv.URL + "/caa-image.jpg",
						},
						"image": srv.URL + "/caa-image-full.jpg",
					},
				},
			})

		// Cover art image download
		case path == "/caa-image.jpg":
			w.Header().Set("Content-Type", "image/jpeg")
			w.Write([]byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00})

		default:
			http.NotFound(w, r)
		}
	}))

	t.Cleanup(srv.Close)
	return srv
}

// newTestMatcher creates a Matcher wired to the given test DB and mock server.
// The MBClient's lastReq is set to zero so throttle() never sleeps.
func newTestMatcher(t *testing.T, db *sql.DB, srv *httptest.Server) *Matcher {
	t.Helper()
	dataDir := t.TempDir()
	thumbDir := filepath.Join(dataDir, "thumbs", "music")
	if err := os.MkdirAll(thumbDir, 0o755); err != nil {
		t.Fatalf("mkdir thumbs: %v", err)
	}

	return &Matcher{
		db:      db,
		dataDir: dataDir,
		mb: &MBClient{
			client:          srv.Client(),
			baseURL:         srv.URL + "/ws/2",
			wikiClient:      srv.Client(),
			wikiBaseURL:     srv.URL,
			wikidataBaseURL: srv.URL,
			commonsBaseURL:  srv.URL,
			lastReq:         time.Time{},
		},
		caa: &CAAClient{
			client:   srv.Client(),
			baseURL:  srv.URL,
			thumbDir: thumbDir,
		},
	}
}

// seedTestData inserts a library, artist, album, track, and scan_jobs row.
// Returns libraryID, artistID, albumID, jobID.
func seedTestData(t *testing.T, db *sql.DB) (int64, int64, int64, int64) {
	t.Helper()
	ctx := context.Background()

	// Library
	res, err := db.ExecContext(ctx,
		"INSERT INTO libraries (name, type, root_path) VALUES ('test', 'music', '/music')")
	if err != nil {
		t.Fatalf("insert library: %v", err)
	}
	libID, _ := res.LastInsertId()

	// Artist (no mbid, no bio, no art -- needs enrichment)
	res, err = db.ExecContext(ctx,
		"INSERT INTO music_artists (library_id, name) VALUES (?, 'Pink Floyd')", libID)
	if err != nil {
		t.Fatalf("insert artist: %v", err)
	}
	artistID, _ := res.LastInsertId()

	// Album (empty match_status -- needs matching)
	res, err = db.ExecContext(ctx, `
		INSERT INTO music_albums (library_id, artist_id, album_artist_id, title, year, match_status)
		VALUES (?, ?, ?, 'Dark Side of the Moon', 1973, '')
	`, libID, artistID, artistID)
	if err != nil {
		t.Fatalf("insert album: %v", err)
	}
	albumID, _ := res.LastInsertId()

	// Track (satisfies FK)
	_, err = db.ExecContext(ctx, `
		INSERT INTO music_tracks (library_id, artist_id, album_id, title, abs_path, track_no, disc_no)
		VALUES (?, ?, ?, 'Breathe', '/fake/track1.mp3', 1, 1)
	`, libID, artistID, albumID)
	if err != nil {
		t.Fatalf("insert track: %v", err)
	}

	// Scan job (music_match, status running)
	res, err = db.ExecContext(ctx, `
		INSERT INTO scan_jobs (library_id, job_type, status, created_by)
		VALUES (?, 'music_match', 'running', 'test')
	`, libID)
	if err != nil {
		t.Fatalf("insert job: %v", err)
	}
	jobID, _ := res.LastInsertId()

	return libID, artistID, albumID, jobID
}

func TestRunMusicMatchIntegrationHappyPath(t *testing.T) {
	db := openTestDB(t)
	srv := newMockMusicServer(t)
	m := newTestMatcher(t, db, srv)
	ctx := context.Background()

	libID, artistID, albumID, jobID := seedTestData(t, db)

	// Run the full pipeline.
	if err := m.RunMusicMatch(ctx, jobID, libID); err != nil {
		t.Fatalf("RunMusicMatch: %v", err)
	}

	// --- Verify artist enrichment ---
	t.Run("artist_mbid", func(t *testing.T) {
		var mbid string
		if err := db.QueryRowContext(ctx,
			"SELECT musicbrainz_id FROM music_artists WHERE id=?", artistID).Scan(&mbid); err != nil {
			t.Fatalf("query artist: %v", err)
		}
		if mbid != "test-artist-mbid" {
			t.Fatalf("expected musicbrainz_id='test-artist-mbid', got %q", mbid)
		}
	})

	t.Run("artist_bio", func(t *testing.T) {
		var bio string
		if err := db.QueryRowContext(ctx,
			"SELECT bio FROM music_artists WHERE id=?", artistID).Scan(&bio); err != nil {
			t.Fatalf("query artist bio: %v", err)
		}
		if !strings.Contains(bio, "Test artist biography text") {
			t.Fatalf("expected bio to contain 'Test artist biography text', got %q", bio)
		}
	})

	t.Run("artist_art", func(t *testing.T) {
		var artPath string
		if err := db.QueryRowContext(ctx,
			"SELECT art_path FROM music_artists WHERE id=?", artistID).Scan(&artPath); err != nil {
			t.Fatalf("query artist art: %v", err)
		}
		if artPath == "" {
			t.Fatal("expected art_path to be non-empty")
		}
		if _, err := os.Stat(artPath); err != nil {
			t.Fatalf("artist art file does not exist: %v", err)
		}
	})

	// --- Verify album matching ---
	t.Run("album_match_status", func(t *testing.T) {
		var matchStatus string
		if err := db.QueryRowContext(ctx,
			"SELECT match_status FROM music_albums WHERE id=?", albumID).Scan(&matchStatus); err != nil {
			t.Fatalf("query album: %v", err)
		}
		if matchStatus != "matched" {
			t.Fatalf("expected match_status='matched', got %q", matchStatus)
		}
	})

	t.Run("album_musicbrainz_id", func(t *testing.T) {
		var mbid string
		if err := db.QueryRowContext(ctx,
			"SELECT musicbrainz_id FROM music_albums WHERE id=?", albumID).Scan(&mbid); err != nil {
			t.Fatalf("query album mbid: %v", err)
		}
		if mbid != "test-rg-id" {
			t.Fatalf("expected musicbrainz_id='test-rg-id', got %q", mbid)
		}
	})

	t.Run("album_match_confidence", func(t *testing.T) {
		var confidence float64
		if err := db.QueryRowContext(ctx,
			"SELECT match_confidence FROM music_albums WHERE id=?", albumID).Scan(&confidence); err != nil {
			t.Fatalf("query album confidence: %v", err)
		}
		if confidence <= 0 {
			t.Fatalf("expected match_confidence > 0, got %f", confidence)
		}
	})

	t.Run("album_match_source", func(t *testing.T) {
		var source string
		if err := db.QueryRowContext(ctx,
			"SELECT match_source FROM music_albums WHERE id=?", albumID).Scan(&source); err != nil {
			t.Fatalf("query album source: %v", err)
		}
		if source != "musicbrainz" {
			t.Fatalf("expected match_source='musicbrainz', got %q", source)
		}
	})

	// --- Verify cover art ---
	t.Run("album_art", func(t *testing.T) {
		var artPath string
		if err := db.QueryRowContext(ctx,
			"SELECT art_path FROM music_albums WHERE id=?", albumID).Scan(&artPath); err != nil {
			t.Fatalf("query album art: %v", err)
		}
		if artPath == "" {
			t.Fatal("expected album art_path to be non-empty")
		}
		if _, err := os.Stat(artPath); err != nil {
			t.Fatalf("album art file does not exist: %v", err)
		}
	})

	// --- Verify job progress ---
	t.Run("job_progress", func(t *testing.T) {
		var total, current int
		if err := db.QueryRowContext(ctx,
			"SELECT progress_total, progress_current FROM scan_jobs WHERE id=?", jobID).Scan(&total, &current); err != nil {
			t.Fatalf("query job: %v", err)
		}
		if total != 1 {
			t.Fatalf("expected progress_total=1, got %d", total)
		}
		if current != 1 {
			t.Fatalf("expected progress_current=1, got %d", current)
		}
	})

	_ = libID // used in RunMusicMatch call
}

func TestRunMusicMatchIntegrationPartialFailure(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// Create a mock server where artist search fails (500) but release-group
	// search and CAA work normally.
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		switch {
		// Artist search returns 500 (simulates enrichment failure)
		case path == "/ws/2/artist" && strings.Contains(r.URL.RawQuery, "query="):
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)

		// Release-group search succeeds
		case path == "/ws/2/release-group" && strings.Contains(r.URL.RawQuery, "query="):
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"release-groups": []map[string]interface{}{
					{
						"id":                 "test-rg-id",
						"title":              "Dark Side of the Moon",
						"primary-type":       "Album",
						"score":              100,
						"first-release-date": "1973-03-01",
						"artist-credit": []map[string]interface{}{
							{
								"name": "Pink Floyd",
								"artist": map[string]string{
									"id":   "test-artist-mbid",
									"name": "Pink Floyd",
								},
							},
						},
						"releases": []map[string]string{
							{"id": "test-release-id", "title": "Dark Side of the Moon"},
						},
					},
				},
			})

		// CAA release-group
		case path == "/release-group/test-rg-id":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"images": []map[string]interface{}{
					{
						"types": []string{"Front"},
						"front": true,
						"thumbnails": map[string]string{
							"large": srv.URL + "/caa-image.jpg",
						},
						"image": srv.URL + "/caa-image-full.jpg",
					},
				},
			})

		// Cover art image
		case path == "/caa-image.jpg":
			w.Header().Set("Content-Type", "image/jpeg")
			w.Write([]byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00})

		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	m := newTestMatcher(t, db, srv)
	libID, artistID, albumID, jobID := seedTestData(t, db)

	// RunMusicMatch should NOT return error (artist enrichment errors are non-fatal).
	if err := m.RunMusicMatch(ctx, jobID, libID); err != nil {
		t.Fatalf("RunMusicMatch should not fail on artist enrichment error: %v", err)
	}

	// Artist enrichment should have failed -- MBID still empty.
	t.Run("artist_not_enriched", func(t *testing.T) {
		var mbid string
		if err := db.QueryRowContext(ctx,
			"SELECT musicbrainz_id FROM music_artists WHERE id=?", artistID).Scan(&mbid); err != nil {
			t.Fatalf("query artist: %v", err)
		}
		if mbid != "" {
			t.Fatalf("expected artist musicbrainz_id to be empty (enrichment failed), got %q", mbid)
		}
	})

	// Album matching should have succeeded independently.
	t.Run("album_matched_despite_artist_failure", func(t *testing.T) {
		var matchStatus string
		if err := db.QueryRowContext(ctx,
			"SELECT match_status FROM music_albums WHERE id=?", albumID).Scan(&matchStatus); err != nil {
			t.Fatalf("query album: %v", err)
		}
		if matchStatus != "matched" && matchStatus != "uncertain" {
			t.Fatalf("expected match_status='matched' or 'uncertain', got %q", matchStatus)
		}
	})

	_ = fmt.Sprint(libID, jobID) // suppress unused warnings
}
