package web

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// seedMusicData inserts a complete music data set in FK-safe order.
// Returns libraryID, artistID, albumID, trackID.
func seedMusicData(t *testing.T, db *sql.DB) (int64, int64, int64, int64) {
	t.Helper()

	res, err := db.Exec(`INSERT INTO libraries (name, type, root_path) VALUES ('Test Music', 'music', '/test/music')`)
	if err != nil {
		t.Fatalf("insert library: %v", err)
	}
	libID, _ := res.LastInsertId()

	res, err = db.Exec(`INSERT INTO music_artists (library_id, name, bio, bio_source_url) VALUES (?, 'Test Artist', '', '')`, libID)
	if err != nil {
		t.Fatalf("insert artist: %v", err)
	}
	artistID, _ := res.LastInsertId()

	res, err = db.Exec(`INSERT INTO music_albums (library_id, artist_id, album_artist_id, title, year, is_compilation) VALUES (?, ?, ?, 'Test Album', 2024, 0)`, libID, artistID, artistID)
	if err != nil {
		t.Fatalf("insert album: %v", err)
	}
	albumID, _ := res.LastInsertId()

	res, err = db.Exec(`INSERT INTO music_tracks (library_id, artist_id, album_id, title, track_no, disc_no, abs_path, mime_type) VALUES (?, ?, ?, 'Track 1', 1, 1, '/test/track1.mp3', 'audio/mpeg')`, libID, artistID, albumID)
	if err != nil {
		t.Fatalf("insert track: %v", err)
	}
	trackID, _ := res.LastInsertId()

	return libID, artistID, albumID, trackID
}

func TestMusicHandlers(t *testing.T) {
	h, db := newTestHandler(t)
	router := h.Router()
	_, artistID, albumID, _ := seedMusicData(t, db)

	tests := []struct {
		name       string
		method     string
		path       string
		wantStatus int
	}{
		{
			name:       "GET /music 200",
			method:     http.MethodGet,
			path:       "/music",
			wantStatus: http.StatusOK,
		},
		{
			name:       "POST /music 405",
			method:     http.MethodPost,
			path:       "/music",
			wantStatus: http.StatusMethodNotAllowed,
		},
		{
			name:       "GET /music/artist/{id} 200",
			method:     http.MethodGet,
			path:       fmt.Sprintf("/music/artist/%d", artistID),
			wantStatus: http.StatusOK,
		},
		{
			name:       "GET /music/artist/999 404",
			method:     http.MethodGet,
			path:       "/music/artist/999",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "GET /music/artist/abc 404",
			method:     http.MethodGet,
			path:       "/music/artist/abc",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "POST /music/artist/{id} 405",
			method:     http.MethodPost,
			path:       fmt.Sprintf("/music/artist/%d", artistID),
			wantStatus: http.StatusMethodNotAllowed,
		},
		{
			name:       "GET /music/album/{id} 200",
			method:     http.MethodGet,
			path:       fmt.Sprintf("/music/album/%d", albumID),
			wantStatus: http.StatusOK,
		},
		{
			name:       "GET /music/album/999 404",
			method:     http.MethodGet,
			path:       "/music/album/999",
			wantStatus: http.StatusNotFound,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("got status %d, want %d", rec.Code, tc.wantStatus)
			}

			// For 404 responses, verify no internal details leak.
			if tc.wantStatus == http.StatusNotFound {
				body := rec.Body.String()
				lower := strings.ToLower(body)
				for _, leak := range []string{"sql:", "sqlite", "file:", "err:", "panic"} {
					if strings.Contains(lower, leak) {
						t.Errorf("404 body contains internal detail %q: %s", leak, body)
					}
				}
			}
		})
	}
}

// TestMusicAlbumsPagination verifies the COUNT + LIMIT/OFFSET + pageNav wiring:
// 65 albums over a page size of 60 → page 1 has 60 (next, no prev), page 2 has 5
// (prev, no next), and an out-of-range page clamps to the last page.
func TestMusicAlbumsPagination(t *testing.T) {
	h, db := newTestHandler(t)
	libID, artistID, _, _ := seedMusicData(t, db) // seeds 1 album
	for i := 0; i < 64; i++ {
		if _, err := db.Exec(
			`INSERT INTO music_albums (library_id, artist_id, album_artist_id, title, year, is_compilation) VALUES (?,?,?,?,2000,0)`,
			libID, artistID, artistID, fmt.Sprintf("Album %02d", i)); err != nil {
			t.Fatalf("insert album %d: %v", i, err)
		}
	}
	router := h.Router()
	get := func(path string) string {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", path, rec.Code)
		}
		return rec.Body.String()
	}

	b1 := get("/music/albums")
	if n := strings.Count(b1, `class="album"`); n != listPageSize {
		t.Fatalf("page 1 album count = %d, want %d", n, listPageSize)
	}
	if !strings.Contains(b1, ">1/2<") {
		t.Fatalf("page 1 nav not 1/2: %s", b1)
	}
	if !strings.Contains(b1, `class="next"`) || strings.Contains(b1, `class="prev"`) {
		t.Fatalf("page 1 should have next and no prev")
	}

	b2 := get("/music/albums?page=2")
	if n := strings.Count(b2, `class="album"`); n != 5 {
		t.Fatalf("page 2 album count = %d, want 5", n)
	}
	if !strings.Contains(b2, `class="prev"`) || strings.Contains(b2, `class="next"`) {
		t.Fatalf("page 2 should have prev and no next")
	}

	if n := strings.Count(get("/music/albums?page=9"), `class="album"`); n != 5 {
		t.Fatalf("out-of-range page should clamp to the last page (5 albums), got %d", n)
	}
}

// TestMusicAlbumsSearch verifies the ?q= filter: it narrows both the COUNT and
// the page query (so total-pages reflects the filter), matches album title OR
// artist name, and preserves q in the page-link query.
func TestMusicAlbumsSearch(t *testing.T) {
	h, db := newTestHandler(t)
	libID, artistID, _, _ := seedMusicData(t, db) // 'Test Album' by 'Test Artist'
	for i := 0; i < 64; i++ {
		if _, err := db.Exec(
			`INSERT INTO music_albums (library_id, artist_id, album_artist_id, title, year, is_compilation) VALUES (?,?,?,?,2000,0)`,
			libID, artistID, artistID, fmt.Sprintf("Album %02d", i)); err != nil {
			t.Fatalf("insert album %d: %v", i, err)
		}
	}
	router := h.Router()
	get := func(path string) string {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", path, rec.Code)
		}
		return rec.Body.String()
	}

	// Title match: "Album 0" matches "Album 00".."Album 09" = 10, one page.
	b := get("/music/albums?q=Album+0")
	if n := strings.Count(b, `class="album"`); n != 10 {
		t.Fatalf("q=Album 0 matched %d albums, want 10", n)
	}
	if !strings.Contains(b, ">1/1<") {
		t.Fatalf("filtered list should be a single page: %s", b)
	}

	// Artist-name match: "Test Artist" is the album_artist of all 65, so a query
	// matching only the artist name (no album title contains it) returns all 65
	// → page 1 caps at listPageSize with a next page, and the term is preserved in
	// the page-link query (the `+` is HTML-escaped to &#43; in this text stub).
	b2 := get("/music/albums?q=test+artist")
	if n := strings.Count(b2, `class="album"`); n != listPageSize {
		t.Fatalf("q=test artist matched %d on page 1, want %d (all via artist name)", n, listPageSize)
	}
	if !strings.Contains(b2, ">1/2<") {
		t.Fatalf("65 artist-name matches should be 2 pages: %s", b2)
	}
	if !strings.Contains(b2, "q=test") {
		t.Fatalf("page Query should preserve the search term: %s", b2)
	}

	// No match → empty page, still one page.
	if n := strings.Count(get("/music/albums?q=zzzznope"), `class="album"`); n != 0 {
		t.Fatalf("no-match query returned %d albums, want 0", n)
	}
}
