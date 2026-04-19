package match

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	isodb "isomedia/internal/db"
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

var testAlbumCounter int64

func insertTestAlbum(t *testing.T, db *sql.DB, libraryID int64, artist, title string, year, trackCount int, artPath, matchStatus string) int64 {
	t.Helper()
	ctx := context.Background()

	// Ensure artist.
	_, err := db.ExecContext(ctx,
		"INSERT OR IGNORE INTO music_artists (library_id, name) VALUES (?, ?)",
		libraryID, artist)
	if err != nil {
		t.Fatalf("insert artist: %v", err)
	}
	var artistID int64
	if err := db.QueryRowContext(ctx,
		"SELECT id FROM music_artists WHERE library_id=? AND name=?",
		libraryID, artist).Scan(&artistID); err != nil {
		t.Fatalf("get artist: %v", err)
	}

	testAlbumCounter++
	albumN := testAlbumCounter

	// Insert album.
	res, err := db.ExecContext(ctx, `
		INSERT INTO music_albums (library_id, artist_id, album_artist_id, title, year, art_path, match_status)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, libraryID, artistID, artistID, title, year, artPath, matchStatus)
	if err != nil {
		t.Fatalf("insert album: %v", err)
	}
	albumID, _ := res.LastInsertId()

	// Insert dummy tracks with unique abs_path values.
	for i := 1; i <= trackCount; i++ {
		absPath := fmt.Sprintf("/fake/album%d/track%d.mp3", albumN, i)
		_, err := db.ExecContext(ctx, `
			INSERT INTO music_tracks (library_id, artist_id, album_id, title, abs_path, track_no, disc_no)
			VALUES (?, ?, ?, ?, ?, ?, 1)
		`, libraryID, artistID, albumID, fmt.Sprintf("Track %d", i), absPath, i)
		if err != nil {
			t.Fatalf("insert track: %v", err)
		}
	}

	return albumID
}

func TestFindDuplicateAlbums(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// Create a library.
	res, err := db.ExecContext(ctx,
		"INSERT INTO libraries (name, type, root_path) VALUES ('test', 'music', '/music')")
	if err != nil {
		t.Fatalf("insert library: %v", err)
	}
	libID, _ := res.LastInsertId()

	// Insert duplicate albums.
	id1 := insertTestAlbum(t, db, libID, "Pink Floyd", "Dark Side of the Moon", 1973, 10, "/art/dsotm.jpg", "matched")
	id2 := insertTestAlbum(t, db, libID, "Pink Floyd", "Dark Side of the Moon (2011 Remaster)", 2011, 10, "", "")
	// Non-duplicate.
	_ = insertTestAlbum(t, db, libID, "Pink Floyd", "The Wall", 1979, 26, "", "")

	groups, err := FindDuplicateAlbums(ctx, db, libID)
	if err != nil {
		t.Fatalf("FindDuplicateAlbums: %v", err)
	}

	if len(groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(groups))
	}

	g := groups[0]
	if len(g.Albums) != 2 {
		t.Fatalf("expected 2 albums in group, got %d", len(g.Albums))
	}

	// Best should be id1 (has art, matched).
	if g.BestAlbumID != id1 {
		t.Fatalf("expected best=%d, got %d", id1, g.BestAlbumID)
	}

	_ = id2
}

func TestMergeAlbums(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	res, err := db.ExecContext(ctx,
		"INSERT INTO libraries (name, type, root_path) VALUES ('test', 'music', '/music')")
	if err != nil {
		t.Fatalf("insert library: %v", err)
	}
	libID, _ := res.LastInsertId()

	targetID := insertTestAlbum(t, db, libID, "The Beatles", "Abbey Road", 1969, 5, "/art/ar.jpg", "matched")
	sourceID := insertTestAlbum(t, db, libID, "The Beatles", "Abbey Road [Remastered]", 2009, 3, "", "")

	if err := MergeAlbums(ctx, db, targetID, sourceID); err != nil {
		t.Fatalf("MergeAlbums: %v", err)
	}

	// Source album should be gone.
	var count int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM music_albums WHERE id=?", sourceID).Scan(&count); err != nil {
		t.Fatalf("query: %v", err)
	}
	if count != 0 {
		t.Fatal("source album still exists after merge")
	}

	// All tracks should be on the target.
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM music_tracks WHERE album_id=?", targetID).Scan(&count); err != nil {
		t.Fatalf("query tracks: %v", err)
	}
	if count != 8 {
		t.Fatalf("expected 8 tracks on target, got %d", count)
	}
}
