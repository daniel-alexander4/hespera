package web

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
)

func artistArtUploadBody(t *testing.T, artistID, filename string, content []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("artist_id", artistID)
	fw, err := mw.CreateFormFile("art", filename)
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	_, _ = fw.Write(content)
	_ = mw.Close()
	return &buf, mw.FormDataContentType()
}

func TestMusicArtistArtGET(t *testing.T) {
	h, db := newTestHandler(t)
	router := h.Router()
	_, artistID, _, _ := seedMusicData(t, db)
	id := strconv.FormatInt(artistID, 10)

	cases := []struct {
		name string
		path string
		want int
	}{
		{"ok", "/music/artist/art?id=" + id, http.StatusOK},
		{"bad id", "/music/artist/art?id=abc", http.StatusNotFound},
		{"unknown", "/music/artist/art?id=999999", http.StatusNotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, c.path, nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != c.want {
				t.Fatalf("status = %d, want %d", rec.Code, c.want)
			}
		})
	}
}

func TestMusicArtistArtClear(t *testing.T) {
	h, db := newTestHandler(t)
	router := h.Router()
	_, artistID, _, _ := seedMusicData(t, db)
	if _, err := db.Exec("UPDATE music_artists SET art_path='/x.jpg' WHERE id=?", artistID); err != nil {
		t.Fatal(err)
	}
	rec := postForm(t, router, "/music/artist/art", url.Values{
		"artist_id": {strconv.FormatInt(artistID, 10)}, "clear": {"1"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("clear status = %d, want 303", rec.Code)
	}
	var ap string
	db.QueryRow("SELECT art_path FROM music_artists WHERE id=?", artistID).Scan(&ap)
	if ap != "" {
		t.Fatalf("art_path = %q, want empty after clear", ap)
	}
}

// SSRF guard: a posted art_url that isn't a current candidate is rejected. With
// no provider keys the candidate set is empty, so any URL is rejected and no
// write happens.
func TestMusicArtistArtURLRejectsNonCandidate(t *testing.T) {
	h, db := newTestHandler(t)
	router := h.Router()
	_, artistID, _, _ := seedMusicData(t, db)
	if _, err := db.Exec("UPDATE music_artists SET musicbrainz_id=? WHERE id=?",
		"11111111-1111-1111-1111-111111111111", artistID); err != nil {
		t.Fatal(err)
	}
	rec := postForm(t, router, "/music/artist/art", url.Values{
		"artist_id": {strconv.FormatInt(artistID, 10)},
		"art_url":   {"http://evil.example/x.jpg"},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("non-candidate url status = %d, want 400", rec.Code)
	}
	var ap string
	db.QueryRow("SELECT art_path FROM music_artists WHERE id=?", artistID).Scan(&ap)
	if ap != "" {
		t.Fatalf("art_path should be unset after a rejected URL, got %q", ap)
	}
}

func TestMusicArtistArtUpload(t *testing.T) {
	h, db := newTestHandler(t)
	router := h.Router()
	_, artistID, _, _ := seedMusicData(t, db)
	id := strconv.FormatInt(artistID, 10)

	// Valid PNG → 303, art_path set.
	body, ct := artistArtUploadBody(t, id, "a.png", onePxPNG)
	req := httptest.NewRequest(http.MethodPost, "/music/artist/art", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("png upload status = %d, want 303 (%s)", rec.Code, rec.Body.String())
	}
	var ap string
	db.QueryRow("SELECT art_path FROM music_artists WHERE id=?", artistID).Scan(&ap)
	if ap == "" {
		t.Fatalf("art_path not set after a valid upload")
	}

	// Disallowed format (GIF is a valid image but not jpeg/png/webp) → 400.
	body, ct = artistArtUploadBody(t, id, "a.gif", tinyGIF)
	req = httptest.NewRequest(http.MethodPost, "/music/artist/art", body)
	req.Header.Set("Content-Type", ct)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("gif upload status = %d, want 400", rec.Code)
	}
}
