package moviescan

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// insertMovieFileSig inserts a movie_files row with the given move-relink
// signature (size, mtime) and returns its id. The file on disk is created
// only when present is true.
func insertMovieFileSig(t *testing.T, db *sql.DB, libID int64, absPath string, size, mtime int64, present bool) int64 {
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
		`INSERT INTO movie_files (library_id, abs_path, container, file_size_bytes, mtime_unix) VALUES (?, ?, 'mkv', ?, ?)`,
		libID, absPath, size, mtime,
	)
	if err != nil {
		t.Fatalf("insertMovieFileSig: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func TestRelinkMovedMovieFiles(t *testing.T) {
	ctx := context.Background()

	t.Run("transfers match and progress to moved file", func(t *testing.T) {
		db := openTestDB(t)
		root := t.TempDir()
		libID := seedLibrary(t, db, "movies", "movies", root)
		s := &Scanner{DB: db}

		oldPath := filepath.Join(root, "Movie (2019)", "old.mkv")
		newPath := filepath.Join(root, "Movie (2019)", "new.mkv")
		oldID := insertMovieFileSig(t, db, libID, oldPath, 12345, 99999, false)
		newID := insertMovieFileSig(t, db, libID, newPath, 12345, 99999, true)

		// Orphan: a match plus resume progress — both irreplaceable.
		if _, err := db.Exec(`UPDATE movie_files SET tmdb_id=42, match_status='matched', match_confidence=1.0, match_source='manual', matched_at='2020-01-01' WHERE id=?`, oldID); err != nil {
			t.Fatalf("seed old match: %v", err)
		}
		if _, err := db.Exec(`INSERT INTO movie_playback_progress (file_id, position_seconds, duration_seconds, completed) VALUES (?, 123, 456, 0)`, oldID); err != nil {
			t.Fatalf("seed progress: %v", err)
		}

		if err := s.relinkMovedFiles(ctx, libID, root); err != nil {
			t.Fatalf("relinkMovedFiles: %v", err)
		}

		var status, source string
		var tmdbID int64
		if err := db.QueryRow(`SELECT match_status, tmdb_id, match_source FROM movie_files WHERE id=?`, newID).
			Scan(&status, &tmdbID, &source); err != nil {
			t.Fatalf("read new row: %v", err)
		}
		if status != "matched" || tmdbID != 42 || source != "manual" {
			t.Fatalf("match not transferred: status=%q tmdb=%d source=%q", status, tmdbID, source)
		}
		var pos float64
		if err := db.QueryRow(`SELECT position_seconds FROM movie_playback_progress WHERE file_id=?`, newID).Scan(&pos); err != nil {
			t.Fatalf("read new progress: %v", err)
		}
		if pos != 123 {
			t.Fatalf("progress not transferred: pos=%v", pos)
		}

		// Prune then confirm the orphan is gone and the survivor keeps the match.
		if err := s.pruneMissingFiles(ctx, libID, root); err != nil {
			t.Fatalf("pruneMissingFiles: %v", err)
		}
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM movie_files WHERE id=?`, oldID).Scan(&n); err != nil {
			t.Fatalf("count old: %v", err)
		}
		if n != 0 {
			t.Fatalf("orphan file not pruned")
		}
		if err := db.QueryRow(`SELECT match_status FROM movie_files WHERE id=?`, newID).Scan(&status); err != nil {
			t.Fatalf("survivor gone after prune: %v", err)
		}
		if status != "matched" {
			t.Fatalf("survivor lost match after prune: %q", status)
		}
	})

	t.Run("ambiguous signature does not transfer", func(t *testing.T) {
		db := openTestDB(t)
		root := t.TempDir()
		libID := seedLibrary(t, db, "movies", "movies", root)
		s := &Scanner{DB: db}

		oldID := insertMovieFileSig(t, db, libID, filepath.Join(root, "old.mkv"), 5000, 7000, false)
		a := insertMovieFileSig(t, db, libID, filepath.Join(root, "a.mkv"), 5000, 7000, true)
		b := insertMovieFileSig(t, db, libID, filepath.Join(root, "b.mkv"), 5000, 7000, true)
		if _, err := db.Exec(`UPDATE movie_files SET tmdb_id=42, match_status='matched' WHERE id=?`, oldID); err != nil {
			t.Fatalf("seed old match: %v", err)
		}

		if err := s.relinkMovedFiles(ctx, libID, root); err != nil {
			t.Fatalf("relinkMovedFiles: %v", err)
		}

		for _, id := range []int64{a, b} {
			var status string
			if err := db.QueryRow(`SELECT match_status FROM movie_files WHERE id=?`, id).Scan(&status); err != nil {
				t.Fatalf("read row %d: %v", id, err)
			}
			if status != "" {
				t.Fatalf("ambiguous signature transferred a match to %d: %q", id, status)
			}
		}
	})
}
