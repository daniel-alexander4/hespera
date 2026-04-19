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
