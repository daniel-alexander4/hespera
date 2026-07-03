package scan

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// insertTrack inserts artist/album (once) and a music_tracks row with the given
// content signature, returning the track id. The file on disk is created only
// when present is true.
func insertTrack(t *testing.T, db *sql.DB, libID, artistID, albumID int64, absPath string, size int64, checksum string, present bool) int64 {
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
		`INSERT INTO music_tracks (library_id, artist_id, album_id, title, abs_path, file_size_bytes, checksum_sha256)
		 VALUES (?, ?, ?, 'Song', ?, ?, ?)`,
		libID, artistID, albumID, absPath, size, checksum,
	)
	if err != nil {
		t.Fatalf("insertTrack: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func seedArtistAlbum(t *testing.T, db *sql.DB, libID int64) (int64, int64) {
	t.Helper()
	artistID := seedArtist(t, db, libID, "Artist")
	albumID := seedAlbum(t, db, libID, artistID, "Album", 0, false)
	return artistID, albumID
}

func TestRelinkMovedTracks(t *testing.T) {
	ctx := context.Background()

	t.Run("transfers play history to moved track", func(t *testing.T) {
		db := openTestDB(t)
		root := t.TempDir()
		libID := seedLibrary(t, db, "music", "music", root)
		artistID, albumID := seedArtistAlbum(t, db, libID)
		s := &Scanner{DB: db}

		oldID := insertTrack(t, db, libID, artistID, albumID, filepath.Join(root, "a", "old.mp3"), 4242, "deadbeef", false)
		newID := insertTrack(t, db, libID, artistID, albumID, filepath.Join(root, "b", "new.mp3"), 4242, "deadbeef", true)

		if _, err := db.Exec(`INSERT INTO play_history (track_id, library_id, artist_id, album_id, played_ms, completed) VALUES (?, ?, ?, ?, 60000, 1)`,
			oldID, libID, artistID, albumID); err != nil {
			t.Fatalf("seed play_history: %v", err)
		}
		if _, err := db.Exec(`INSERT INTO lyrics_cache (track_id, provider_key, lyrics_text) VALUES (?, 'lrclib', 'la la')`, oldID); err != nil {
			t.Fatalf("seed lyrics_cache: %v", err)
		}
		if _, err := db.Exec(`INSERT INTO playlists (name) VALUES ('P')`); err != nil {
			t.Fatalf("seed playlist: %v", err)
		}
		if _, err := db.Exec(`INSERT INTO playlist_tracks (playlist_id, track_id, position) VALUES (1, ?, 1)`, oldID); err != nil {
			t.Fatalf("seed playlist_tracks: %v", err)
		}

		if err := s.relinkMovedTracks(ctx, libID, root); err != nil {
			t.Fatalf("relinkMovedTracks: %v", err)
		}

		var trackID int64
		if err := db.QueryRow(`SELECT track_id FROM play_history`).Scan(&trackID); err != nil {
			t.Fatalf("read play_history: %v", err)
		}
		if trackID != newID {
			t.Fatalf("play_history not re-pointed: got %d want %d", trackID, newID)
		}
		if err := db.QueryRow(`SELECT track_id FROM lyrics_cache`).Scan(&trackID); err != nil {
			t.Fatalf("read lyrics_cache: %v", err)
		}
		if trackID != newID {
			t.Fatalf("lyrics_cache not re-pointed: got %d want %d", trackID, newID)
		}
		if err := db.QueryRow(`SELECT track_id FROM playlist_tracks`).Scan(&trackID); err != nil {
			t.Fatalf("read playlist_tracks: %v", err)
		}
		if trackID != newID {
			t.Fatalf("playlist_tracks not re-pointed: got %d want %d", trackID, newID)
		}

		// Prune the orphan; the re-pointed history must survive the cascade.
		if err := s.pruneMissingTracks(ctx, libID, root); err != nil {
			t.Fatalf("pruneMissingTracks: %v", err)
		}
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM music_tracks WHERE id=?`, oldID).Scan(&n); err != nil {
			t.Fatalf("count old: %v", err)
		}
		if n != 0 {
			t.Fatalf("orphan track not pruned")
		}
		if err := db.QueryRow(`SELECT COUNT(*) FROM play_history`).Scan(&n); err != nil {
			t.Fatalf("count play_history: %v", err)
		}
		if n != 1 {
			t.Fatalf("play_history lost on prune: count=%d", n)
		}
	})

	t.Run("ambiguous signature does not transfer", func(t *testing.T) {
		db := openTestDB(t)
		root := t.TempDir()
		libID := seedLibrary(t, db, "music", "music", root)
		artistID, albumID := seedArtistAlbum(t, db, libID)
		s := &Scanner{DB: db}

		oldID := insertTrack(t, db, libID, artistID, albumID, filepath.Join(root, "old.mp3"), 100, "dupe", false)
		insertTrack(t, db, libID, artistID, albumID, filepath.Join(root, "a.mp3"), 100, "dupe", true)
		insertTrack(t, db, libID, artistID, albumID, filepath.Join(root, "b.mp3"), 100, "dupe", true)

		if _, err := db.Exec(`INSERT INTO play_history (track_id, library_id, artist_id, album_id) VALUES (?, ?, ?, ?)`,
			oldID, libID, artistID, albumID); err != nil {
			t.Fatalf("seed play_history: %v", err)
		}

		if err := s.relinkMovedTracks(ctx, libID, root); err != nil {
			t.Fatalf("relinkMovedTracks: %v", err)
		}
		var trackID int64
		if err := db.QueryRow(`SELECT track_id FROM play_history`).Scan(&trackID); err != nil {
			t.Fatalf("read play_history: %v", err)
		}
		if trackID != oldID {
			t.Fatalf("ambiguous match wrongly re-pointed: got %d", trackID)
		}
	})

	t.Run("empty checksum is never matched", func(t *testing.T) {
		db := openTestDB(t)
		root := t.TempDir()
		libID := seedLibrary(t, db, "music", "music", root)
		artistID, albumID := seedArtistAlbum(t, db, libID)
		s := &Scanner{DB: db}

		oldID := insertTrack(t, db, libID, artistID, albumID, filepath.Join(root, "old.mp3"), 200, "", false)
		newID := insertTrack(t, db, libID, artistID, albumID, filepath.Join(root, "new.mp3"), 200, "", true)

		if _, err := db.Exec(`INSERT INTO play_history (track_id, library_id, artist_id, album_id) VALUES (?, ?, ?, ?)`,
			oldID, libID, artistID, albumID); err != nil {
			t.Fatalf("seed play_history: %v", err)
		}

		if err := s.relinkMovedTracks(ctx, libID, root); err != nil {
			t.Fatalf("relinkMovedTracks: %v", err)
		}
		var trackID int64
		if err := db.QueryRow(`SELECT track_id FROM play_history`).Scan(&trackID); err != nil {
			t.Fatalf("read play_history: %v", err)
		}
		if trackID != oldID {
			t.Fatalf("empty-checksum rows wrongly matched: got %d want %d", trackID, newID)
		}
	})
}
