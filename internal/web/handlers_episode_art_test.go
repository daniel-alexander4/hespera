package web

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// TestEpisodeArt pins the /art/episode/{id} contract: pending/unavailable
// rows 404 (the template renders its own placeholder), a stored path serves
// with nosniff, and a path outside the data dir is refused (the pathguard
// containment every art handler applies).
func TestEpisodeArt(t *testing.T) {
	h, db := newTestHandler(t)

	if _, err := db.Exec("INSERT INTO libraries (id, name, type, root_path) VALUES (1, 'TV', 'tv', '/tv')"); err != nil {
		t.Fatalf("insert library: %v", err)
	}
	thumbDir := filepath.Join(h.cfg.DataDir, "thumbs", "episodes")
	if err := os.MkdirAll(thumbDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	thumb := filepath.Join(thumbDir, "ep_1.webp")
	if err := os.WriteFile(thumb, []byte("webp-bytes"), 0o644); err != nil {
		t.Fatalf("write thumb: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "evil.webp")
	if err := os.WriteFile(outside, []byte("evil"), 0o644); err != nil {
		t.Fatalf("write outside: %v", err)
	}

	rows := []struct {
		id        int64
		thumbPath string
		wantCode  int
	}{
		{1, thumb, 200},
		{2, "", 404},
		{3, "unavailable", 404},
		{4, outside, 404},
	}
	for _, r := range rows {
		if _, err := db.Exec(
			"INSERT INTO tv_series_files (id, library_id, abs_path, thumb_path) VALUES (?, 1, ?, ?)",
			r.id, filepath.Join("/tv", "e"+strconv.FormatInt(r.id, 10)+".mkv"), r.thumbPath); err != nil {
			t.Fatalf("insert file %d: %v", r.id, err)
		}
	}

	for _, r := range rows {
		req := httptest.NewRequest("GET", "/art/episode/"+strconv.FormatInt(r.id, 10), nil)
		rec := httptest.NewRecorder()
		h.episodeArt(rec, req)
		if rec.Code != r.wantCode {
			t.Fatalf("id %d: status %d, want %d", r.id, rec.Code, r.wantCode)
		}
		if r.wantCode == 200 {
			if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Fatalf("id %d: nosniff header = %q", r.id, got)
			}
			if rec.Body.String() != "webp-bytes" {
				t.Fatalf("id %d: wrong body served", r.id)
			}
		}
	}

	// Unknown id → 404.
	req := httptest.NewRequest("GET", "/art/episode/999", nil)
	rec := httptest.NewRecorder()
	h.episodeArt(rec, req)
	if rec.Code != 404 {
		t.Fatalf("unknown id: status %d, want 404", rec.Code)
	}
}
