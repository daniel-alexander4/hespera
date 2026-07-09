package moviescan

import (
	"context"
	"path/filepath"
	"testing"

	"hespera/internal/config"
)

// TestScanMoviesSkipsPruneOnEmptyRoot: a walk that finds no files while the
// library has rows (the unmounted-mount-point case) must NOT prune — pruning
// would delete every row and the match/progress state only rows carry.
func TestScanMoviesSkipsPruneOnEmptyRoot(t *testing.T) {
	db := openTestDB(t)
	root := t.TempDir() // exists but empty — an unmounted mount point looks exactly like this
	libID := seedLibrary(t, db, "movies", "movies", root)
	fileID := insertMovieFileSig(t, db, libID, filepath.Join(root, "Film (2020)", "film.mkv"), 100, 200, false)

	s := New(config.Config{MediaRoot: root}, db)
	if err := s.ScanMovies(context.Background(), 0, libID); err != nil {
		t.Fatalf("ScanMovies: %v", err)
	}

	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM movie_files WHERE id=?", fileID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("file pruned on empty-root scan — the prune-safety guard failed")
	}
}
