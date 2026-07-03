package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"hespera/internal/video"
)

// Trickplay serving: assets come from the file's content-keyed cache dir,
// asset names are whitelisted, and a missing sprite set is a plain 404.

func TestStreamTrickplay(t *testing.T) {
	h, db := newTestHandler(t)
	router := h.Router()

	res, err := db.Exec("INSERT INTO libraries (name, type, root_path) VALUES ('TV', 'tv', ?)", h.cfg.MediaRoot)
	if err != nil {
		t.Fatal(err)
	}
	libID, _ := res.LastInsertId()
	fres, err := db.Exec(
		"INSERT INTO tv_series_files (library_id, abs_path, container, file_size_bytes, mtime_unix) VALUES (?, ?, 'mkv', 111, 222)",
		libID, filepath.Join(h.cfg.MediaRoot, "ep.mkv"))
	if err != nil {
		t.Fatal(err)
	}
	fileID, _ := fres.LastInsertId()

	get := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	// No sprite set yet → 404 (the player silently degrades).
	if rec := get(fmt.Sprintf("/stream/tv-trickplay/%d/manifest.json", fileID)); rec.Code != http.StatusNotFound {
		t.Fatalf("missing set should 404, got %d", rec.Code)
	}

	// Materialize a cache dir under the file's content key and serve from it.
	key := video.TrickplayKey(filepath.Join(h.cfg.MediaRoot, "ep.mkv"), time.Unix(222, 0), 111)
	dir := filepath.Join(h.trickplayCacheRoot(), key)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := video.TrickplayManifest{IntervalSec: 10, Width: 240, Height: 100, Tile: 5, Frames: 42}
	b, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sprite00000.jpg"), []byte("jpegbytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	rec := get(fmt.Sprintf("/stream/tv-trickplay/%d/manifest.json", fileID))
	if rec.Code != http.StatusOK {
		t.Fatalf("manifest: %d", rec.Code)
	}
	var m video.TrickplayManifest
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil || m.Frames != 42 {
		t.Fatalf("manifest body wrong: %v %+v", err, m)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("nosniff missing")
	}
	if rec := get(fmt.Sprintf("/stream/tv-trickplay/%d/sprite00000.jpg", fileID)); rec.Code != http.StatusOK || rec.Body.String() != "jpegbytes" {
		t.Fatalf("sprite serve wrong: %d", rec.Code)
	}

	// Whitelist: arbitrary names never resolve. (Dot-dot traversal never even
	// reaches the handler — ServeMux path-cleaning 301s it away first.)
	for _, bad := range []string{"sprite1.jpg", "manifest.json.bak", "sprite00000.png", "..%2Fsecrets"} {
		if rec := get(fmt.Sprintf("/stream/tv-trickplay/%d/%s", fileID, bad)); rec.Code != http.StatusNotFound {
			t.Fatalf("asset %q should 404, got %d", bad, rec.Code)
		}
	}
	if rec := get(fmt.Sprintf("/stream/tv-trickplay/%d/../../secrets", fileID)); rec.Code != http.StatusMovedPermanently {
		t.Fatalf("dot-dot should be mux-cleaned (301), got %d", rec.Code)
	}
	// Unknown file id → 404.
	if rec := get("/stream/tv-trickplay/99999/manifest.json"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown file should 404, got %d", rec.Code)
	}
}

// The session JSON now carries raw chapters (all of them — classification is
// the skip system's concern, not the tick layer's).
func TestChapterMarks(t *testing.T) {
	probe := &video.ProbeResult{Chapters: []video.ProbeChapter{
		{StartSec: 0, EndSec: 90, Title: "Opening Credits"},
		{StartSec: 90, EndSec: 1200, Title: "Chapter 1"},
	}}
	marks := chapterMarks(probe)
	if len(marks) != 2 || marks[1].Start != 90 || marks[1].Title != "Chapter 1" {
		t.Fatalf("chapterMarks = %+v", marks)
	}
	if chapterMarks(nil) != nil {
		t.Fatal("nil probe → nil marks")
	}
}
