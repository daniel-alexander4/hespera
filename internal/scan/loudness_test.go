package scan

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"hespera/internal/config"
	"hespera/internal/jobs"
)

// TestAnalyzeLoudnessSelectsOnlyUnanalyzed verifies the candidate query (rows
// missing EITHER measurement — loudness_lufs=0, or loudness_tp=0 for a row
// analyzed before the true-peak column existed, which is the one-shot backfill),
// progress wiring, and the graceful missing-file skip — all without ffmpeg (the
// reprobe-test pattern).
func TestAnalyzeLoudnessSelectsOnlyUnanalyzed(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	root := t.TempDir()
	libID := seedLibrary(t, db, "Music", "music", root)
	s := &Scanner{Cfg: config.Config{MediaRoot: root}, DB: db}

	res, err := db.Exec(
		"INSERT INTO scan_jobs (library_id, job_type, status, progress_current, progress_total, created_at) VALUES (?, 'music_loudness', 'running', 0, 0, datetime('now'))",
		libID)
	if err != nil {
		t.Fatalf("insert scan_jobs: %v", err)
	}
	jobID, _ := res.LastInsertId()

	a := seedArtist(t, db, libID, "L Artist")
	al := seedAlbum(t, db, libID, a, "L Album", 2020, false)
	// Candidate: unanalyzed, file missing on disk → selected, skipped gracefully.
	seedTrack(t, db, libID, a, al, "Quiet", 1, filepath.Join(root, "a", "quiet.mp3"))
	// Candidate: has loudness but no true peak — a row measured before the column
	// existed. This is the backfill, and missing it would leave the track unable
	// to be boosted forever (an unmeasured peak is never spent).
	seedTrack(t, db, libID, a, al, "Backfill", 2, filepath.Join(root, "a", "backfill.mp3"))
	if _, err := db.Exec("UPDATE music_tracks SET loudness_lufs=-9.5, loudness_tp=0 WHERE title='Backfill'"); err != nil {
		t.Fatal(err)
	}
	// Non-candidate: both measurements present; must be untouched.
	seedTrack(t, db, libID, a, al, "Done", 3, filepath.Join(root, "a", "done.mp3"))
	if _, err := db.Exec("UPDATE music_tracks SET loudness_lufs=-12.5, loudness_tp=-1.4 WHERE title='Done'"); err != nil {
		t.Fatal(err)
	}

	if err := s.AnalyzeLoudness(ctx, jobID, libID); err != nil {
		t.Fatalf("AnalyzeLoudness: %v", err)
	}

	var total int
	if err := db.QueryRow("SELECT progress_total FROM scan_jobs WHERE id=?", jobID).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Fatalf("progress_total = %d, want 2 (the unanalyzed row + the true-peak backfill)", total)
	}
	var lufs, tp float64
	if err := db.QueryRow("SELECT loudness_lufs, loudness_tp FROM music_tracks WHERE title='Done'").Scan(&lufs, &tp); err != nil {
		t.Fatal(err)
	}
	if lufs != -12.5 || tp != -1.4 {
		t.Fatalf("fully-analyzed row changed: lufs=%v tp=%v", lufs, tp)
	}
}

// TestAnalyzeLoudnessYields verifies the sweep stops with jobs.ErrYielded when
// interactive work is queued (ShouldYield true) — after processing at least one
// item, so an interrupted sweep always makes progress — and never yields when
// the hook is unset. No ffmpeg: missing files skip before any process spawns.
func TestAnalyzeLoudnessYields(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	root := t.TempDir()
	libID := seedLibrary(t, db, "Music", "music", root)
	s := &Scanner{Cfg: config.Config{MediaRoot: root}, DB: db}
	s.ShouldYield = func() bool { return true }

	res, err := db.Exec(
		"INSERT INTO scan_jobs (library_id, job_type, status, progress_current, progress_total, created_at) VALUES (?, 'music_loudness', 'running', 0, 0, datetime('now'))",
		libID)
	if err != nil {
		t.Fatalf("insert scan_jobs: %v", err)
	}
	jobID, _ := res.LastInsertId()

	a := seedArtist(t, db, libID, "Y Artist")
	al := seedAlbum(t, db, libID, a, "Y Album", 2020, false)
	seedTrack(t, db, libID, a, al, "One", 1, filepath.Join(root, "y", "one.mp3"))
	seedTrack(t, db, libID, a, al, "Two", 2, filepath.Join(root, "y", "two.mp3"))

	if err := s.AnalyzeLoudness(ctx, jobID, libID); !errors.Is(err, jobs.ErrYielded) {
		t.Fatalf("AnalyzeLoudness with queued interactive work = %v, want jobs.ErrYielded", err)
	}

	// The yield flushed the real progress (1 item processed before the i>0
	// check fired), so the requeued row never reads a stale 0/N.
	var cur int
	if err := db.QueryRow("SELECT progress_current FROM scan_jobs WHERE id=?", jobID).Scan(&cur); err != nil {
		t.Fatal(err)
	}
	if cur != 1 {
		t.Fatalf("progress_current after yield = %d, want 1 (flushed before ErrYielded)", cur)
	}

	// Hook unset → the same sweep runs to completion (missing files just skip).
	s.ShouldYield = nil
	if err := s.AnalyzeLoudness(ctx, jobID, libID); err != nil {
		t.Fatalf("AnalyzeLoudness without hook: %v", err)
	}
}
