package tvscan

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// insertTVFile inserts a tv_series_files row with the given signature and
// returns its id. The file on disk is created only when present is true.
func insertTVFile(t *testing.T, db *sql.DB, libID int64, absPath string, size, mtime int64, present bool) int64 {
	t.Helper()
	if present {
		if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := os.WriteFile(absPath, []byte("x"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}
	res, err := db.Exec(
		`INSERT INTO tv_series_files (library_id, abs_path, container, file_size_bytes, mtime_unix) VALUES (?, ?, 'mkv', ?, ?)`,
		libID, absPath, size, mtime,
	)
	if err != nil {
		t.Fatalf("insertTVFile: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func TestRelinkMovedFiles(t *testing.T) {
	ctx := context.Background()

	t.Run("transfers match and progress to moved file", func(t *testing.T) {
		db := openTestDB(t)
		root := t.TempDir()
		libID := seedLibrary(t, db, "tv", "tv", root)
		s := &Scanner{DB: db}

		oldPath := filepath.Join(root, "Show", "Season 01", "old.mkv")
		newPath := filepath.Join(root, "Show", "Season 01", "new.mkv")
		oldID := insertTVFile(t, db, libID, oldPath, 12345, 99999, false)
		newID := insertTVFile(t, db, libID, newPath, 12345, 99999, true)

		// Orphan: a manual match plus resume progress — both irreplaceable.
		if _, err := db.Exec(`INSERT INTO tv_series_identities
			(file_id, status, provider, series_id, season_number, episode_numbers_csv, match_confidence, match_method, matched_at, guessed_title, air_date)
			VALUES (?, 'matched', 'tmdb', '42', 1, '1', 1.0, 'manual', '2020-01-01', 'Show', '')`, oldID); err != nil {
			t.Fatalf("seed old identity: %v", err)
		}
		if _, err := db.Exec(`INSERT INTO tv_playback_progress (file_id, position_seconds, duration_seconds, completed) VALUES (?, 123, 456, 0)`, oldID); err != nil {
			t.Fatalf("seed progress: %v", err)
		}
		// Survivor: fresh unmatched identity, as a scan would have just written.
		if _, err := db.Exec(`INSERT INTO tv_series_identities (file_id, status, guessed_title, season_number, episode_numbers_csv) VALUES (?, 'unmatched', 'Show', 1, '1')`, newID); err != nil {
			t.Fatalf("seed new identity: %v", err)
		}

		if err := s.relinkMovedFiles(ctx, libID, root); err != nil {
			t.Fatalf("relinkMovedFiles: %v", err)
		}

		var status, seriesID, method string
		if err := db.QueryRow(`SELECT status, series_id, match_method FROM tv_series_identities WHERE file_id=?`, newID).
			Scan(&status, &seriesID, &method); err != nil {
			t.Fatalf("read new identity: %v", err)
		}
		if status != "matched" || seriesID != "42" || method != "manual" {
			t.Fatalf("identity not transferred: status=%q series=%q method=%q", status, seriesID, method)
		}
		var pos float64
		if err := db.QueryRow(`SELECT position_seconds FROM tv_playback_progress WHERE file_id=?`, newID).Scan(&pos); err != nil {
			t.Fatalf("read new progress: %v", err)
		}
		if pos != 123 {
			t.Fatalf("progress not transferred: pos=%v", pos)
		}

		// Prune then confirm the orphan and its cascaded rows are gone.
		if err := s.pruneMissingFiles(ctx, libID, root); err != nil {
			t.Fatalf("pruneMissingFiles: %v", err)
		}
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM tv_series_files WHERE id=?`, oldID).Scan(&n); err != nil {
			t.Fatalf("count old: %v", err)
		}
		if n != 0 {
			t.Fatalf("orphan file not pruned")
		}
		if err := db.QueryRow(`SELECT status FROM tv_series_identities WHERE file_id=?`, newID).Scan(&status); err != nil {
			t.Fatalf("new identity gone after prune: %v", err)
		}
		if status != "matched" {
			t.Fatalf("new identity lost match after prune: %q", status)
		}
	})

	t.Run("ambiguous signature does not transfer", func(t *testing.T) {
		db := openTestDB(t)
		root := t.TempDir()
		libID := seedLibrary(t, db, "tv", "tv", root)
		s := &Scanner{DB: db}

		oldID := insertTVFile(t, db, libID, filepath.Join(root, "old.mkv"), 5000, 7000, false)
		a := insertTVFile(t, db, libID, filepath.Join(root, "a.mkv"), 5000, 7000, true)
		b := insertTVFile(t, db, libID, filepath.Join(root, "b.mkv"), 5000, 7000, true)

		if _, err := db.Exec(`INSERT INTO tv_series_identities (file_id, status, series_id, guessed_title) VALUES (?, 'matched', '99', 'X')`, oldID); err != nil {
			t.Fatalf("seed: %v", err)
		}
		for _, id := range []int64{a, b} {
			if _, err := db.Exec(`INSERT INTO tv_series_identities (file_id, status, guessed_title) VALUES (?, 'unmatched', 'X')`, id); err != nil {
				t.Fatalf("seed survivor: %v", err)
			}
		}

		if err := s.relinkMovedFiles(ctx, libID, root); err != nil {
			t.Fatalf("relinkMovedFiles: %v", err)
		}
		for _, id := range []int64{a, b} {
			var status string
			if err := db.QueryRow(`SELECT status FROM tv_series_identities WHERE file_id=?`, id).Scan(&status); err != nil {
				t.Fatalf("read: %v", err)
			}
			if status != "unmatched" {
				t.Fatalf("ambiguous match wrongly transferred to %d: %q", id, status)
			}
		}
	})

	t.Run("changed mtime falls back to no transfer", func(t *testing.T) {
		db := openTestDB(t)
		root := t.TempDir()
		libID := seedLibrary(t, db, "tv", "tv", root)
		s := &Scanner{DB: db}

		oldID := insertTVFile(t, db, libID, filepath.Join(root, "old.mkv"), 8000, 1000, false)
		newID := insertTVFile(t, db, libID, filepath.Join(root, "new.mkv"), 8000, 2000, true) // mtime differs (cp)

		if _, err := db.Exec(`INSERT INTO tv_series_identities (file_id, status, series_id, guessed_title) VALUES (?, 'matched', '77', 'Y')`, oldID); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if _, err := db.Exec(`INSERT INTO tv_series_identities (file_id, status, guessed_title) VALUES (?, 'unmatched', 'Y')`, newID); err != nil {
			t.Fatalf("seed survivor: %v", err)
		}

		if err := s.relinkMovedFiles(ctx, libID, root); err != nil {
			t.Fatalf("relinkMovedFiles: %v", err)
		}
		var status string
		if err := db.QueryRow(`SELECT status FROM tv_series_identities WHERE file_id=?`, newID).Scan(&status); err != nil {
			t.Fatalf("read: %v", err)
		}
		if status != "unmatched" {
			t.Fatalf("transfer happened despite mtime mismatch: %q", status)
		}
	})
}
