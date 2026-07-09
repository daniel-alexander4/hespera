package tvscan

import (
	"context"
	"path/filepath"
	"testing"

	"hespera/internal/config"
)

// TestScanTVSkipsPruneOnEmptyRoot: a walk that finds no files while the
// library has rows (the unmounted-mount-point case) must NOT prune — pruning
// would delete every row and the match/progress state only rows carry.
func TestScanTVSkipsPruneOnEmptyRoot(t *testing.T) {
	db := openTestDB(t)
	root := t.TempDir() // exists but empty — an unmounted mount point looks exactly like this
	libID := seedLibrary(t, db, "tv", "tv", root)
	fileID := insertTVFile(t, db, libID, filepath.Join(root, "Show", "Season 01", "e1.mkv"), 100, 200, false)

	s := New(config.Config{MediaRoot: root}, db)
	if err := s.ScanTV(context.Background(), 0, libID); err != nil {
		t.Fatalf("ScanTV: %v", err)
	}

	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM tv_series_files WHERE id=?", fileID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("file pruned on empty-root scan — the prune-safety guard failed")
	}
}
