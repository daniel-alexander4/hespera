package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// When an album's files are missing on disk (folder moved/renamed without a
// rescan), the edit POST must report them as "moved" so the page can show an
// actionable "run a Scan first" message — not a bare error, and never a silent
// reload that looks like the edit didn't save.
func TestMusicAlbumEditReportsMovedFiles(t *testing.T) {
	h, db := newTestHandler(t)
	router := h.Router()
	_, _, albumID, _ := seedMusicData(t, db) // track abs_path /test/track1.mp3 does not exist
	idStr := strconv.FormatInt(albumID, 10)

	rec := postForm(t, router, "/music/album/edit?id="+idStr, url.Values{
		"title":  {"Test Album"},
		"artist": {"Test Artist"},
		"year":   {"2024"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "/music/album/edit") {
		t.Fatalf("should redirect back to the edit form, got %q", loc)
	}
	if !strings.Contains(loc, "moved=1") {
		t.Fatalf("Location = %q, want a moved=1 count (missing file reported as moved, not a bare error)", loc)
	}
}

// Album-level required fields still gate before any file work.
func TestMusicAlbumEditRequiresTitleAndArtist(t *testing.T) {
	h, db := newTestHandler(t)
	router := h.Router()
	_, _, albumID, _ := seedMusicData(t, db)
	idStr := strconv.FormatInt(albumID, 10)

	rec := postForm(t, router, "/music/album/edit?id="+idStr, url.Values{"title": {"Only Title"}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (missing artist)", rec.Code)
	}
}
