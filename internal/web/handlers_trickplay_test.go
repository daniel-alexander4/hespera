package web

import (
	"context"
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

// TestStreamTrickplayTouchesDir pins the LRU contract: serving an asset
// refreshes the cache dir's mtime so size-cap eviction is by last use.
func TestStreamTrickplayTouchesDir(t *testing.T) {
	h, db := newTestHandler(t)
	router := h.Router()
	res, _ := db.Exec("INSERT INTO libraries (name, type, root_path) VALUES ('TV', 'tv', ?)", h.cfg.MediaRoot)
	libID, _ := res.LastInsertId()
	fres, _ := db.Exec(
		"INSERT INTO tv_series_files (library_id, abs_path, container, file_size_bytes, mtime_unix) VALUES (?, ?, 'mkv', 111, 222)",
		libID, filepath.Join(h.cfg.MediaRoot, "ep.mkv"))
	fileID, _ := fres.LastInsertId()

	key := video.TrickplayKey(filepath.Join(h.cfg.MediaRoot, "ep.mkv"), time.Unix(222, 0), 111)
	dir := filepath.Join(h.trickplayCacheRoot(), key)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-100 * time.Hour)
	if err := os.Chtimes(dir, old, old); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/stream/tv-trickplay/%d/manifest.json", fileID), nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("manifest: %d", rec.Code)
	}
	st, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(st.ModTime()) > time.Minute {
		t.Fatalf("serve did not touch the dir (mtime %v)", st.ModTime())
	}
}

// TestSweepTrickplayOrphans pins the orphan sweep's safety contract: keys
// claimed by ANY tv or movie file — any library — survive; unclaimed old dirs
// are removed; unclaimed young dirs are spared (racing-writer guard).
func TestSweepTrickplayOrphans(t *testing.T) {
	h, db := newTestHandler(t)
	res, _ := db.Exec("INSERT INTO libraries (name, type, root_path) VALUES ('TV', 'tv', ?)", h.cfg.MediaRoot)
	tvLib, _ := res.LastInsertId()
	res, _ = db.Exec("INSERT INTO libraries (name, type, root_path) VALUES ('Films', 'movies', ?)", h.cfg.MediaRoot)
	movLib, _ := res.LastInsertId()
	tvPath := filepath.Join(h.cfg.MediaRoot, "ep.mkv")
	movPath := filepath.Join(h.cfg.MediaRoot, "film.mkv")
	if _, err := db.Exec(
		"INSERT INTO tv_series_files (library_id, abs_path, container, file_size_bytes, mtime_unix) VALUES (?, ?, 'mkv', 111, 222)",
		tvLib, tvPath); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		"INSERT INTO movie_files (library_id, abs_path, container, file_size_bytes, mtime_unix) VALUES (?, ?, 'mkv', 333, 444)",
		movLib, movPath); err != nil {
		t.Fatal(err)
	}

	root := h.trickplayCacheRoot()
	mkdir := func(name string, age time.Duration) string {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		when := time.Now().Add(-age)
		if err := os.Chtimes(dir, when, when); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	tvClaimed := mkdir(video.TrickplayKey(tvPath, time.Unix(222, 0), 111), 48*time.Hour)
	movClaimed := mkdir(video.TrickplayKey(movPath, time.Unix(444, 0), 333), 48*time.Hour)
	orphanOld := mkdir("00000000deadbeef", 48*time.Hour)
	orphanYoung := mkdir("00000000cafebabe", time.Minute)

	h.sweepTrickplayOrphans(context.Background(), root)

	for _, keep := range []string{tvClaimed, movClaimed, orphanYoung} {
		if _, err := os.Stat(keep); err != nil {
			t.Errorf("sweep removed %s, want kept", filepath.Base(keep))
		}
	}
	if _, err := os.Stat(orphanOld); !os.IsNotExist(err) {
		t.Error("sweep kept the old orphan, want removed")
	}
}

// TestGenerateTrickplayBailsAtCap pins the size budget: when the cache root is
// already at the cap, the job skips generation entirely (no ffmpeg spawn — the
// pruner would evict whatever it built) instead of feeding the churn.
func TestGenerateTrickplayBailsAtCap(t *testing.T) {
	h, db := newTestHandler(t)
	h.cfg.TrickplayCacheMaxBytes = 1
	res, _ := db.Exec("INSERT INTO libraries (name, type, root_path) VALUES ('TV', 'tv', ?)", h.cfg.MediaRoot)
	libID, _ := res.LastInsertId()
	src := filepath.Join(h.cfg.MediaRoot, "ep.mkv")
	if err := os.WriteFile(src, []byte("not a real video"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		"INSERT INTO tv_series_files (library_id, abs_path, container, file_size_bytes, mtime_unix) VALUES (?, ?, 'mkv', 16, 222)",
		libID, src); err != nil {
		t.Fatal(err)
	}
	// Pre-existing cache content puts the root over the 1-byte cap. Recent
	// mtime so the post-run orphan sweep's age guard spares it.
	filler := filepath.Join(h.trickplayCacheRoot(), "0000000000000000")
	if err := os.MkdirAll(filler, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filler, "manifest.json"), []byte("xx"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := h.generateTrickplayMissing(context.Background(), "tv_series_files", 0, libID); err != nil {
		t.Fatalf("generateTrickplayMissing: %v", err)
	}
	key := video.TrickplayKey(src, time.Unix(222, 0), 16)
	if _, err := os.Stat(filepath.Join(h.trickplayCacheRoot(), key)); !os.IsNotExist(err) {
		t.Fatal("generation ran despite the cache being at cap")
	}
}
