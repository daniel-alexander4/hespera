package scan

import (
	"context"
	"database/sql"
	"testing"
)

// seedArtist inserts a music_artists row and returns its ID.
func seedArtist(t *testing.T, db *sql.DB, libraryID int64, name string) int64 {
	t.Helper()
	res, err := db.Exec(
		"INSERT INTO music_artists (library_id, name) VALUES (?, ?) ON CONFLICT(library_id, name) DO NOTHING",
		libraryID, name,
	)
	if err != nil {
		t.Fatalf("seedArtist insert: %v", err)
	}
	id, _ := res.LastInsertId()
	if id == 0 {
		// ON CONFLICT hit; fetch existing ID
		if err := db.QueryRow("SELECT id FROM music_artists WHERE library_id=? AND name=?", libraryID, name).Scan(&id); err != nil {
			t.Fatalf("seedArtist select: %v", err)
		}
	}
	return id
}

// seedAlbum inserts a music_albums row and returns its ID.
func seedAlbum(t *testing.T, db *sql.DB, libraryID, artistID int64, title string, year int, isCompilation bool) int64 {
	t.Helper()
	comp := 0
	if isCompilation {
		comp = 1
	}
	res, err := db.Exec(
		"INSERT INTO music_albums (library_id, artist_id, album_artist_id, title, year, is_compilation) VALUES (?, ?, ?, ?, ?, ?)",
		libraryID, artistID, artistID, title, year, comp,
	)
	if err != nil {
		t.Fatalf("seedAlbum: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

// seedTrack inserts a music_tracks row.
func seedTrack(t *testing.T, db *sql.DB, libraryID, artistID, albumID int64, title string, trackNo int, absPath string) {
	t.Helper()
	_, err := db.Exec(
		`INSERT INTO music_tracks (library_id, artist_id, album_id, title, track_no, disc_no, abs_path, mime_type, file_size_bytes, mtime_unix, checksum_sha256)
		 VALUES (?, ?, ?, ?, ?, 1, ?, 'audio/mpeg', 1000, 1000000, 'abc123')`,
		libraryID, artistID, albumID, title, trackNo, absPath,
	)
	if err != nil {
		t.Fatalf("seedTrack: %v", err)
	}
}

func TestFinalizeCompilations(t *testing.T) {
	t.Run("marks multi-artist album as compilation", func(t *testing.T) {
		db := openTestDB(t)
		libID := seedLibrary(t, db, "Music", "music", "/tmp/music")
		ctx := context.Background()

		artist1 := seedArtist(t, db, libID, "Artist One")
		artist2 := seedArtist(t, db, libID, "Artist Two")
		albumID := seedAlbum(t, db, libID, artist1, "Various Hits", 2020, false)

		seedTrack(t, db, libID, artist1, albumID, "Song A", 1, "/tmp/music/a.mp3")
		seedTrack(t, db, libID, artist2, albumID, "Song B", 2, "/tmp/music/b.mp3")

		scanner := &Scanner{DB: db}
		if err := scanner.finalizeCompilations(ctx, libID); err != nil {
			t.Fatalf("finalizeCompilations: %v", err)
		}

		// Verify is_compilation = 1
		var isComp int
		if err := db.QueryRow("SELECT is_compilation FROM music_albums WHERE id=?", albumID).Scan(&isComp); err != nil {
			t.Fatalf("query is_compilation: %v", err)
		}
		if isComp != 1 {
			t.Fatalf("expected is_compilation=1, got %d", isComp)
		}

		// Verify album artist is now "Various Artists"
		var albumArtistID int64
		if err := db.QueryRow("SELECT album_artist_id FROM music_albums WHERE id=?", albumID).Scan(&albumArtistID); err != nil {
			t.Fatalf("query album_artist_id: %v", err)
		}
		var vaName string
		if err := db.QueryRow("SELECT name FROM music_artists WHERE id=?", albumArtistID).Scan(&vaName); err != nil {
			t.Fatalf("query VA name: %v", err)
		}
		if vaName != "Various Artists" {
			t.Fatalf("expected 'Various Artists', got %q", vaName)
		}
	})

	t.Run("leaves single-artist album unchanged", func(t *testing.T) {
		db := openTestDB(t)
		libID := seedLibrary(t, db, "Music", "music", "/tmp/music")
		ctx := context.Background()

		artist := seedArtist(t, db, libID, "Solo Artist")
		albumID := seedAlbum(t, db, libID, artist, "Solo Album", 2020, false)

		seedTrack(t, db, libID, artist, albumID, "Song 1", 1, "/tmp/music/s1.mp3")
		seedTrack(t, db, libID, artist, albumID, "Song 2", 2, "/tmp/music/s2.mp3")
		seedTrack(t, db, libID, artist, albumID, "Song 3", 3, "/tmp/music/s3.mp3")

		scanner := &Scanner{DB: db}
		if err := scanner.finalizeCompilations(ctx, libID); err != nil {
			t.Fatalf("finalizeCompilations: %v", err)
		}

		var isComp int
		if err := db.QueryRow("SELECT is_compilation FROM music_albums WHERE id=?", albumID).Scan(&isComp); err != nil {
			t.Fatalf("query: %v", err)
		}
		if isComp != 0 {
			t.Fatalf("expected is_compilation=0, got %d", isComp)
		}
	})

	t.Run("merges variant albums", func(t *testing.T) {
		db := openTestDB(t)
		libID := seedLibrary(t, db, "Music", "music", "/tmp/music")
		ctx := context.Background()

		artist1 := seedArtist(t, db, libID, "Artist A")
		artist2 := seedArtist(t, db, libID, "Artist B")
		artist3 := seedArtist(t, db, libID, "Artist C")

		// Two albums with same title+year but different artist_ids.
		// Each must have tracks from multiple artists to trigger compilation detection,
		// which then triggers the merge of same-title+year albums.
		album1 := seedAlbum(t, db, libID, artist1, "Shared Title", 2021, false)
		album2 := seedAlbum(t, db, libID, artist2, "Shared Title", 2021, false)

		// album1 has tracks from artist1 and artist2 (multi-artist)
		seedTrack(t, db, libID, artist1, album1, "Track X", 1, "/tmp/music/x.mp3")
		seedTrack(t, db, libID, artist2, album1, "Track Y", 2, "/tmp/music/y.mp3")
		// album2 has tracks from artist2 and artist3 (multi-artist)
		seedTrack(t, db, libID, artist2, album2, "Track Z", 1, "/tmp/music/z.mp3")
		seedTrack(t, db, libID, artist3, album2, "Track W", 2, "/tmp/music/w.mp3")

		scanner := &Scanner{DB: db}
		if err := scanner.finalizeCompilations(ctx, libID); err != nil {
			t.Fatalf("finalizeCompilations: %v", err)
		}

		// Verify only 1 album remains with this title+year
		var albumCount int
		if err := db.QueryRow(
			"SELECT COUNT(*) FROM music_albums WHERE library_id=? AND lower(title)=lower(?) AND year=?",
			libID, "Shared Title", 2021,
		).Scan(&albumCount); err != nil {
			t.Fatalf("count albums: %v", err)
		}
		// After merge, the variant albums still exist as rows (only tracks are moved),
		// but all tracks should point to the surviving album.
		var trackAlbumIDs []int64
		rows, err := db.Query("SELECT DISTINCT album_id FROM music_tracks WHERE library_id=?", libID)
		if err != nil {
			t.Fatalf("query track album_ids: %v", err)
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				t.Fatalf("scan: %v", err)
			}
			trackAlbumIDs = append(trackAlbumIDs, id)
		}
		if len(trackAlbumIDs) != 1 {
			t.Fatalf("expected all tracks in 1 album, got %d distinct album_ids: %v", len(trackAlbumIDs), trackAlbumIDs)
		}
	})

	t.Run("already-marked compilation not re-processed", func(t *testing.T) {
		db := openTestDB(t)
		libID := seedLibrary(t, db, "Music", "music", "/tmp/music")
		ctx := context.Background()

		artist1 := seedArtist(t, db, libID, "Artist X")
		artist2 := seedArtist(t, db, libID, "Artist Y")
		va := seedArtist(t, db, libID, "Various Artists")

		// Album already marked as compilation
		albumID := seedAlbum(t, db, libID, va, "Already Comp", 2020, true)

		seedTrack(t, db, libID, artist1, albumID, "Track 1", 1, "/tmp/music/ac1.mp3")
		seedTrack(t, db, libID, artist2, albumID, "Track 2", 2, "/tmp/music/ac2.mp3")

		scanner := &Scanner{DB: db}
		if err := scanner.finalizeCompilations(ctx, libID); err != nil {
			t.Fatalf("finalizeCompilations: %v", err)
		}

		// Verify still compilation
		var isComp int
		if err := db.QueryRow("SELECT is_compilation FROM music_albums WHERE id=?", albumID).Scan(&isComp); err != nil {
			t.Fatalf("query: %v", err)
		}
		if isComp != 1 {
			t.Fatalf("expected is_compilation=1, got %d", isComp)
		}

		// Verify no duplicate "Various Artists" entries
		var vaCount int
		if err := db.QueryRow("SELECT COUNT(*) FROM music_artists WHERE library_id=? AND name='Various Artists'", libID).Scan(&vaCount); err != nil {
			t.Fatalf("count VA: %v", err)
		}
		if vaCount != 1 {
			t.Fatalf("expected 1 'Various Artists' row, got %d", vaCount)
		}
	})

	t.Run("rescan consistency", func(t *testing.T) {
		db := openTestDB(t)
		libID := seedLibrary(t, db, "Music", "music", "/tmp/music")
		ctx := context.Background()

		artist1 := seedArtist(t, db, libID, "Rescan A")
		artist2 := seedArtist(t, db, libID, "Rescan B")
		albumID := seedAlbum(t, db, libID, artist1, "Rescan Album", 2022, false)

		seedTrack(t, db, libID, artist1, albumID, "R1", 1, "/tmp/music/r1.mp3")
		seedTrack(t, db, libID, artist2, albumID, "R2", 2, "/tmp/music/r2.mp3")

		scanner := &Scanner{DB: db}

		// First run
		if err := scanner.finalizeCompilations(ctx, libID); err != nil {
			t.Fatalf("first run: %v", err)
		}

		// Snapshot state after first run
		var albums1, artists1, tracks1 int
		db.QueryRow("SELECT COUNT(*) FROM music_albums WHERE library_id=?", libID).Scan(&albums1)
		db.QueryRow("SELECT COUNT(*) FROM music_artists WHERE library_id=?", libID).Scan(&artists1)
		db.QueryRow("SELECT COUNT(*) FROM music_tracks WHERE library_id=?", libID).Scan(&tracks1)

		// Second run
		if err := scanner.finalizeCompilations(ctx, libID); err != nil {
			t.Fatalf("second run: %v", err)
		}

		// Snapshot state after second run
		var albums2, artists2, tracks2 int
		db.QueryRow("SELECT COUNT(*) FROM music_albums WHERE library_id=?", libID).Scan(&albums2)
		db.QueryRow("SELECT COUNT(*) FROM music_artists WHERE library_id=?", libID).Scan(&artists2)
		db.QueryRow("SELECT COUNT(*) FROM music_tracks WHERE library_id=?", libID).Scan(&tracks2)

		if albums1 != albums2 {
			t.Fatalf("album count changed: %d -> %d", albums1, albums2)
		}
		if artists1 != artists2 {
			t.Fatalf("artist count changed: %d -> %d", artists1, artists2)
		}
		if tracks1 != tracks2 {
			t.Fatalf("track count changed: %d -> %d", tracks1, tracks2)
		}
	})
}
