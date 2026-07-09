package scan

import (
	"context"
	"path/filepath"
	"testing"

	"hespera/internal/config"
)

// TestScanMusicSkipsPruneOnEmptyRoot: a walk that finds no files while the
// library has rows (the unmounted-mount-point case) must NOT prune — pruning
// would delete every row and the state only rows carry.
func TestScanMusicSkipsPruneOnEmptyRoot(t *testing.T) {
	db := openTestDB(t)
	root := t.TempDir() // exists but empty — an unmounted mount point looks exactly like this
	libID := seedLibrary(t, db, "music", "music", root)
	artistID, albumID := seedArtistAlbum(t, db, libID)
	trackID := insertTrack(t, db, libID, artistID, albumID, filepath.Join(root, "a", "song.mp3"), 100, "abc", false)

	s := New(config.Config{MediaRoot: root}, db)
	if err := s.ScanMusic(context.Background(), 0, libID); err != nil {
		t.Fatalf("ScanMusic: %v", err)
	}

	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM music_tracks WHERE id=?", trackID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("track pruned on empty-root scan — the prune-safety guard failed")
	}
}
