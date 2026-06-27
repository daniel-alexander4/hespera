package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func postForm(t *testing.T, router http.Handler, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestMusicAlbumArtClear(t *testing.T) {
	h, db := newTestHandler(t)
	router := h.Router()
	_, _, albumID, _ := seedMusicData(t, db)

	// Matched album with art + a check timestamp.
	if _, err := db.Exec(
		"UPDATE music_albums SET match_status='matched', musicbrainz_id='rg-1', art_path='/x/y.jpg', art_checked_at='2026-01-01T00:00:00Z' WHERE id=?",
		albumID); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	rec := postForm(t, router, "/music/album/art/clear", url.Values{"album_id": {strconv.FormatInt(albumID, 10)}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}

	var artPath, checkedAt, status, mbid string
	if err := db.QueryRow("SELECT art_path, art_checked_at, match_status, musicbrainz_id FROM music_albums WHERE id=?", albumID).
		Scan(&artPath, &checkedAt, &status, &mbid); err != nil {
		t.Fatalf("query: %v", err)
	}
	if artPath != "" || checkedAt != "" {
		t.Fatalf("art not cleared: art_path=%q art_checked_at=%q", artPath, checkedAt)
	}
	// Identity is preserved — clear-art only touches the cover.
	if status != "matched" || mbid != "rg-1" {
		t.Fatalf("identity changed: status=%q mbid=%q, want matched/rg-1", status, mbid)
	}
}

func TestMusicAlbumUnmatch(t *testing.T) {
	h, db := newTestHandler(t)
	router := h.Router()
	_, _, albumID, _ := seedMusicData(t, db)

	if _, err := db.Exec(
		"UPDATE music_albums SET match_status='matched', musicbrainz_id='rg-1', artist_musicbrainz_id='ar-1', match_confidence=96, matched_at='2026-01-01T00:00:00Z', art_path='/x/y.jpg', art_checked_at='2026-01-01T00:00:00Z' WHERE id=?",
		albumID); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	rec := postForm(t, router, "/music/album/unmatch", url.Values{"album_id": {strconv.FormatInt(albumID, 10)}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}

	var status, mbid, artistMBID, artPath, checkedAt, matchedAt string
	var conf float64
	if err := db.QueryRow(
		"SELECT match_status, musicbrainz_id, artist_musicbrainz_id, match_confidence, matched_at, art_path, art_checked_at FROM music_albums WHERE id=?",
		albumID).Scan(&status, &mbid, &artistMBID, &conf, &matchedAt, &artPath, &checkedAt); err != nil {
		t.Fatalf("query: %v", err)
	}
	// Everything reset so the next match run re-matches from scratch.
	for name, got := range map[string]string{
		"match_status": status, "musicbrainz_id": mbid, "artist_musicbrainz_id": artistMBID,
		"matched_at": matchedAt, "art_path": artPath, "art_checked_at": checkedAt,
	} {
		if got != "" {
			t.Fatalf("%s = %q, want empty", name, got)
		}
	}
	if conf != 0 {
		t.Fatalf("match_confidence = %v, want 0", conf)
	}
}

func TestMusicAlbumUnmatchEndpointsRejectGET(t *testing.T) {
	h, _ := newTestHandler(t)
	router := h.Router()
	for _, path := range []string{"/music/album/art/clear", "/music/album/unmatch"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s GET = %d, want 405", path, rec.Code)
		}
	}
}
