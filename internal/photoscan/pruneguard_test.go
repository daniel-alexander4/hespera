package photoscan

import (
	"context"
	"path/filepath"
	"testing"

	"hespera/internal/config"
)

// TestScanPhotosSkipsPruneOnEmptyRoot: a walk that finds no files while the
// library has rows (the unmounted-mount-point case) must NOT prune — pruning
// would delete every row and the playback state + thumbs only rows carry.
func TestScanPhotosSkipsPruneOnEmptyRoot(t *testing.T) {
	db := openTestDB(t)
	root := t.TempDir() // exists but empty — an unmounted mount point looks exactly like this
	libID := seedLibrary(t, db, "photos", "home_media", root)
	res, err := db.Exec(
		`INSERT INTO photos (library_id, abs_path, kind, file_size_bytes, mtime_unix) VALUES (?, ?, 'image', 100, 200)`,
		libID, filepath.Join(root, "2021", "pic.jpg"),
	)
	if err != nil {
		t.Fatal(err)
	}
	photoID, _ := res.LastInsertId()

	s := New(config.Config{MediaRoot: root}, db)
	if err := s.ScanPhotos(context.Background(), 0, libID); err != nil {
		t.Fatalf("ScanPhotos: %v", err)
	}

	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM photos WHERE id=?", photoID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("photo pruned on empty-root scan — the prune-safety guard failed")
	}
}
