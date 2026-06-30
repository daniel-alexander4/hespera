package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// tinyWEBP is the smallest byte sequence http.DetectContentType reads as
// image/webp (RIFF + 4 size bytes + WEBP). VerifyImage only sniffs the magic,
// so it passes without a fully-decodable image.
var tinyWEBP = []byte("RIFF\x00\x00\x00\x00WEBPVP8 ")

// TestMovieArtReuploadRemovesOrphan covers the re-upload cleanup: uploading a
// new cover in a different format (png→webp) changes the on-disk filename (it's
// derived from the detected format), so the prior file must be removed rather
// than left to leak until the next match-time thumbgc sweep.
func TestMovieArtReuploadRemovesOrphan(t *testing.T) {
	h, db := newTestHandler(t)
	seedMovieLibrary(t, db, h.cfg.MediaRoot)
	seedMovieFile(t, db, "Fight Club", 1999, "matched", 550, h.cfg.MediaRoot)
	router := h.Router()

	upload := func(filename string, content []byte) {
		t.Helper()
		body, ct := movieArtBody(t, "550", "poster", filename, content)
		req := httptest.NewRequest(http.MethodPost, "/movie/art", body)
		req.Header.Set("Content-Type", ct)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("upload %s = %d, want 303: %s", filename, rec.Code, rec.Body.String())
		}
	}

	upload("p.png", onePxPNG)
	var pngPath string
	if err := db.QueryRow("SELECT art_path FROM movie_art WHERE tmdb_movie_id=550 AND art_type='poster'").Scan(&pngPath); err != nil {
		t.Fatalf("png row: %v", err)
	}
	if _, err := os.Stat(pngPath); err != nil {
		t.Fatalf("png file not written: %v", err)
	}

	upload("p.webp", tinyWEBP)
	var webpPath string
	if err := db.QueryRow("SELECT art_path FROM movie_art WHERE tmdb_movie_id=550 AND art_type='poster'").Scan(&webpPath); err != nil {
		t.Fatalf("webp row: %v", err)
	}
	if webpPath == pngPath {
		t.Fatalf("art_path unchanged (%q) — the extension should have changed", webpPath)
	}
	if _, err := os.Stat(webpPath); err != nil {
		t.Fatalf("webp file not written: %v", err)
	}
	// The superseded PNG must be gone — not left as an orphan.
	if _, err := os.Stat(pngPath); !os.IsNotExist(err) {
		t.Fatalf("orphaned PNG still on disk after re-upload (stat err=%v)", err)
	}
}
