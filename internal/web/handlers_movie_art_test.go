package web

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// movieArtBody builds a multipart upload body for POST /movie/art. (onePxPNG /
// tinyGIF fixtures live in handlers_music_art_test.go, same package.)
func movieArtBody(t *testing.T, tmdbID, artType, filename string, content []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("tmdb_id", tmdbID)
	_ = mw.WriteField("art_type", artType)
	if content != nil {
		fw, err := mw.CreateFormFile("art", filename)
		if err != nil {
			t.Fatalf("CreateFormFile: %v", err)
		}
		_, _ = fw.Write(content)
	}
	_ = mw.Close()
	return &buf, mw.FormDataContentType()
}

func TestMovieArtUpload(t *testing.T) {
	h, db := newTestHandler(t)
	seedMovieLibrary(t, db, h.cfg.MediaRoot)
	seedMovieFile(t, db, "Fight Club", 1999, "matched", 550, h.cfg.MediaRoot)
	router := h.Router()

	// A valid PNG poster → 303 + a manual movie_art row + the file on disk.
	body, ct := movieArtBody(t, "550", "poster", "p.png", onePxPNG)
	req := httptest.NewRequest(http.MethodPost, "/movie/art", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("upload = %d, want 303: %s", rec.Code, rec.Body.String())
	}
	var ap string
	var manual int
	if err := db.QueryRow(
		"SELECT art_path, manual FROM movie_art WHERE tmdb_movie_id=550 AND art_type='poster'").Scan(&ap, &manual); err != nil {
		t.Fatalf("movie_art row not found: %v", err)
	}
	if manual != 1 || ap == "" {
		t.Fatalf("manual=%d art_path=%q, want manual=1 + a path", manual, ap)
	}
	if _, err := os.Stat(ap); err != nil {
		t.Fatalf("art file not written: %v", err)
	}

	// Serving the uploaded bytes sets X-Content-Type-Options: nosniff.
	g := httptest.NewRequest(http.MethodGet, "/art/movie/poster/550", nil)
	gr := httptest.NewRecorder()
	router.ServeHTTP(gr, g)
	if gr.Code != http.StatusOK {
		t.Fatalf("serve = %d", gr.Code)
	}
	if gr.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("movie art serve missing nosniff")
	}

	// A disallowed format (GIF) is rejected.
	bad, ct2 := movieArtBody(t, "550", "poster", "x.gif", tinyGIF)
	br := httptest.NewRequest(http.MethodPost, "/movie/art", bad)
	br.Header.Set("Content-Type", ct2)
	brec := httptest.NewRecorder()
	router.ServeHTTP(brec, br)
	if brec.Code != http.StatusBadRequest {
		t.Fatalf("invalid upload = %d, want 400", brec.Code)
	}

	// A bad art_type is rejected.
	wt, ct3 := movieArtBody(t, "550", "fanart", "p.png", onePxPNG)
	wr := httptest.NewRequest(http.MethodPost, "/movie/art", wt)
	wr.Header.Set("Content-Type", ct3)
	wrec := httptest.NewRecorder()
	router.ServeHTTP(wrec, wr)
	if wrec.Code != http.StatusBadRequest {
		t.Fatalf("bad art_type = %d, want 400", wrec.Code)
	}
}
