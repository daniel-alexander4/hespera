package tvscan

import (
	"context"
	"path/filepath"
	"testing"

	"hespera/internal/config"
)

// TestReprobeMissingSelectsOnlyEmptyRows verifies the candidate query (empty
// stream info, plus rows probed before aspect capture — no display_aspect_ratio
// key), the progress wiring, and that a missing file is skipped gracefully — all
// without ffmpeg. The actual probe write-back on a real file is covered by live
// verification.
func TestReprobeMissingSelectsOnlyEmptyRows(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	root := t.TempDir()
	libID := seedLibrary(t, db, "tv", "tv", root)
	s := &Scanner{Cfg: config.Config{MediaRoot: root}, DB: db}

	res, err := db.Exec(
		"INSERT INTO scan_jobs (library_id, job_type, status, progress_current, progress_total, created_at) VALUES (?, 'tv_probe', 'running', 0, 0, datetime('now'))",
		libID,
	)
	if err != nil {
		t.Fatalf("insert scan_jobs: %v", err)
	}
	jobID, _ := res.LastInsertId()

	// Candidate: default-empty stream info, file missing on disk (so the probe is
	// skipped without ffmpeg — we're testing selection + graceful skip).
	candidate := insertTVFile(t, db, libID, filepath.Join(root, "Show", "S01", "ep1.mkv"), 1, 1, false)
	// Non-candidate: fully probed (has the display_aspect_ratio key); untouched.
	probed := insertTVFile(t, db, libID, filepath.Join(root, "Show", "S01", "ep2.mkv"), 2, 2, false)
	const probedJSON = `{"format":{"duration":"100.0"},"streams":[{"display_aspect_ratio":"16:9"}]}`
	if _, err := db.Exec("UPDATE tv_series_files SET stream_info_json=? WHERE id=?", probedJSON, probed); err != nil {
		t.Fatalf("seed probed row: %v", err)
	}
	// Candidate: probed before aspect capture existed (no display_aspect_ratio
	// key) — the one-shot backfill must re-select it.
	preDAR := insertTVFile(t, db, libID, filepath.Join(root, "Show", "S01", "ep3.mkv"), 3, 3, false)
	if _, err := db.Exec("UPDATE tv_series_files SET stream_info_json=? WHERE id=?", `{"format":{"duration":"100.0"}}`, preDAR); err != nil {
		t.Fatalf("seed pre-DAR row: %v", err)
	}

	if err := s.ReprobeMissing(ctx, jobID, libID); err != nil {
		t.Fatalf("ReprobeMissing: %v", err)
	}

	var total int
	if err := db.QueryRow("SELECT progress_total FROM scan_jobs WHERE id=?", jobID).Scan(&total); err != nil {
		t.Fatalf("read progress_total: %v", err)
	}
	if total != 2 {
		t.Fatalf("progress_total = %d, want 2 (the empty row + the pre-DAR row)", total)
	}

	var got string
	if err := db.QueryRow("SELECT stream_info_json FROM tv_series_files WHERE id=?", probed).Scan(&got); err != nil {
		t.Fatalf("read probed row: %v", err)
	}
	if got != probedJSON {
		t.Fatalf("already-probed row changed: %q", got)
	}

	if err := db.QueryRow("SELECT stream_info_json FROM tv_series_files WHERE id=?", candidate).Scan(&got); err != nil {
		t.Fatalf("read candidate row: %v", err)
	}
	if got != "{}" {
		t.Fatalf("candidate with a missing file should be skipped unchanged, got %q", got)
	}
}

func TestCountNonNativeContainer(t *testing.T) {
	db := openTestDB(t)
	libID := seedLibrary(t, db, "tv", "tv", "/media/tv")
	s := &Scanner{DB: db}
	for _, c := range []string{"mkv", "mp4", "avi", "m4v"} {
		if _, err := db.Exec(
			"INSERT INTO tv_series_files (library_id, abs_path, container, file_size_bytes, mtime_unix) VALUES (?, ?, ?, 1, 1)",
			libID, "/media/tv/"+c+".x", c,
		); err != nil {
			t.Fatalf("insert %s: %v", c, err)
		}
	}
	// mkv + avi are non-native; mp4 + m4v direct-play.
	if n := s.countNonNativeContainer(context.Background(), libID); n != 2 {
		t.Fatalf("countNonNativeContainer = %d, want 2", n)
	}
}
