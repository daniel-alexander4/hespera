package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
)

// The happy path (valid MBID on a real album) re-points identity then makes live
// MusicBrainz/Cover-Art-Archive calls, so it is manual-verify; these cover the
// input validation and guard paths that gate before any network work.

func TestMusicAlbumReassignRejectsBadInput(t *testing.T) {
	h, db := newTestHandler(t)
	router := h.Router()
	_, _, albumID, _ := seedMusicData(t, db)
	idStr := strconv.FormatInt(albumID, 10)
	validMBID := "61cb81de-99e4-3a76-9ec9-69e74c7e5e8f"

	cases := []struct {
		name string
		form url.Values
	}{
		{"missing album_id", url.Values{"release_group_mbid": {validMBID}}},
		{"non-numeric album_id", url.Values{"album_id": {"abc"}, "release_group_mbid": {validMBID}}},
		{"missing mbid", url.Values{"album_id": {idStr}}},
		{"malformed mbid", url.Values{"album_id": {idStr}, "release_group_mbid": {"not-a-uuid"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := postForm(t, router, "/music/album/reassign", c.form)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
		})
	}

	// A bad input must never mutate the album's identity.
	var mbid string
	if err := db.QueryRow("SELECT COALESCE(musicbrainz_id,'') FROM music_albums WHERE id=?", albumID).Scan(&mbid); err != nil {
		t.Fatal(err)
	}
	if mbid != "" {
		t.Fatalf("musicbrainz_id = %q after rejected requests, want empty", mbid)
	}
}

func TestMusicAlbumReassignUnknownAlbum(t *testing.T) {
	h, _ := newTestHandler(t)
	router := h.Router()
	// Valid MBID but a nonexistent album — must 404 before any network work.
	rec := postForm(t, router, "/music/album/reassign", url.Values{
		"album_id":           {"999999"},
		"release_group_mbid": {"61cb81de-99e4-3a76-9ec9-69e74c7e5e8f"},
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestMusicAlbumReassignRejectsGET(t *testing.T) {
	h, _ := newTestHandler(t)
	router := h.Router()
	req := httptest.NewRequest(http.MethodGet, "/music/album/reassign", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET = %d, want 405", rec.Code)
	}
}
