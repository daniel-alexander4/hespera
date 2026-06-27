package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
)

// The rescan handler delegates to scan.ScanFiles (already covered in the scan
// package, including embedded-art extraction). These tests cover the newly
// wired web surface: the route is reachable, the method is gated, and a missing
// album is rejected before any scan work.

func TestMusicAlbumRescanRedirects(t *testing.T) {
	h, db := newTestHandler(t)
	router := h.Router()
	_, _, albumID, _ := seedMusicData(t, db)

	// The seeded track path doesn't exist on disk, so ScanFiles errors
	// internally; the handler logs and still redirects (it never aborts the
	// response). What we assert here is that the route is wired and reachable.
	rec := postForm(t, router, "/music/album/rescan", url.Values{"album_id": {strconv.FormatInt(albumID, 10)}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
}

func TestMusicAlbumRescanMissingAlbum(t *testing.T) {
	h, _ := newTestHandler(t)
	router := h.Router()

	rec := postForm(t, router, "/music/album/rescan", url.Values{"album_id": {"999999"}})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestMusicAlbumRescanRejectsGET(t *testing.T) {
	h, _ := newTestHandler(t)
	router := h.Router()

	req := httptest.NewRequest(http.MethodGet, "/music/album/rescan", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET = %d, want 405", rec.Code)
	}
}
