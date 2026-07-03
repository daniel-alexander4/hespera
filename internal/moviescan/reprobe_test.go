package moviescan

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"hespera/internal/config"
	isodb "hespera/internal/db"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.sqlite")
	conn, err := isodb.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := isodb.Migrate(conn); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return conn
}

func seedLibrary(t *testing.T, db *sql.DB, name, libType, rootPath string) int64 {
	t.Helper()
	res, err := db.Exec(
		"INSERT INTO libraries (name, type, root_path) VALUES (?, ?, ?)",
		name, libType, rootPath,
	)
	if err != nil {
		t.Fatalf("seedLibrary: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func insertMovieFile(t *testing.T, db *sql.DB, libID int64, absPath, streamInfoJSON string) int64 {
	t.Helper()
	res, err := db.Exec(
		"INSERT INTO movie_files (library_id, abs_path, container, file_size_bytes, mtime_unix, stream_info_json) VALUES (?, ?, 'mkv', 1, 1, ?)",
		libID, absPath, streamInfoJSON,
	)
	if err != nil {
		t.Fatalf("insertMovieFile: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

// TestReprobeMissingSelectsOnlyEmptyRows verifies the candidate query (only rows
// with empty stream info), the progress wiring, and that a missing file is
// skipped gracefully — all without ffmpeg. Mirrors the tvscan reprobe test; the
// actual probe write-back on a real file is covered by live verification.
func TestReprobeMissingSelectsOnlyEmptyRows(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	root := t.TempDir()
	libID := seedLibrary(t, db, "movies", "movies", root)
	s := &Scanner{Cfg: config.Config{MediaRoot: root}, DB: db}

	res, err := db.Exec(
		"INSERT INTO scan_jobs (library_id, job_type, status, progress_current, progress_total, created_at) VALUES (?, 'movie_probe', 'running', 0, 0, datetime('now'))",
		libID,
	)
	if err != nil {
		t.Fatalf("insert scan_jobs: %v", err)
	}
	jobID, _ := res.LastInsertId()

	// Candidate: empty stream info, file missing on disk (so the probe is
	// skipped without ffmpeg — we're testing selection + graceful skip).
	candidate := insertMovieFile(t, db, libID, filepath.Join(root, "Movie (2019)", "movie.mkv"), "{}")
	// Non-candidate: already has a duration; must be left untouched.
	const probedJSON = `{"format":{"duration":"100.0"}}`
	probed := insertMovieFile(t, db, libID, filepath.Join(root, "Other (2020)", "other.mkv"), probedJSON)

	if err := s.ReprobeMissing(ctx, jobID, libID); err != nil {
		t.Fatalf("ReprobeMissing: %v", err)
	}

	var total int
	if err := db.QueryRow("SELECT progress_total FROM scan_jobs WHERE id=?", jobID).Scan(&total); err != nil {
		t.Fatalf("read progress_total: %v", err)
	}
	if total != 1 {
		t.Fatalf("progress_total = %d, want 1 (only the empty row is a candidate)", total)
	}

	var got string
	if err := db.QueryRow("SELECT stream_info_json FROM movie_files WHERE id=?", probed).Scan(&got); err != nil {
		t.Fatalf("read probed row: %v", err)
	}
	if got != probedJSON {
		t.Fatalf("already-probed row changed: %q", got)
	}

	if err := db.QueryRow("SELECT stream_info_json FROM movie_files WHERE id=?", candidate).Scan(&got); err != nil {
		t.Fatalf("read candidate row: %v", err)
	}
	if got != "{}" {
		t.Fatalf("candidate with a missing file should be skipped unchanged, got %q", got)
	}
}
