package scan

import (
	"context"
	"database/sql"
	"fmt"
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

	t.Run("dominant-artist album is not promoted to compilation", func(t *testing.T) {
		// 8 tracks by one artist + 1 stray by another: a mis-tagged single-artist
		// album, not a compilation. The strict-majority guard must leave it alone.
		db := openTestDB(t)
		libID := seedLibrary(t, db, "Music", "music", "/tmp/music")
		ctx := context.Background()

		main := seedArtist(t, db, libID, "Main Artist")
		stray := seedArtist(t, db, libID, "Stray Artist")
		albumID := seedAlbum(t, db, libID, main, "Mostly Mine", 1968, false)
		for i := 1; i <= 8; i++ {
			seedTrack(t, db, libID, main, albumID, fmt.Sprintf("M%d", i), i, fmt.Sprintf("/tmp/music/m%d.mp3", i))
		}
		seedTrack(t, db, libID, stray, albumID, "Stray", 9, "/tmp/music/stray.mp3")

		scanner := &Scanner{DB: db}
		if err := scanner.finalizeCompilations(ctx, libID); err != nil {
			t.Fatalf("finalizeCompilations: %v", err)
		}

		var isComp int
		var artistID int64
		if err := db.QueryRow("SELECT is_compilation, artist_id FROM music_albums WHERE id=?", albumID).Scan(&isComp, &artistID); err != nil {
			t.Fatalf("query: %v", err)
		}
		if isComp != 0 {
			t.Fatalf("expected is_compilation=0 (dominant artist), got %d", isComp)
		}
		if artistID != main {
			t.Fatalf("expected album to stay under main artist %d, got %d", main, artistID)
		}
	})

	t.Run("collision with empty Various Artists orphan is resolved", func(t *testing.T) {
		// Reproduces the production deadlock: an empty VA album already occupies
		// (VA, title, year), so reparenting a genuine candidate to VA used to fail
		// on the UNIQUE constraint and abort the whole scan. The orphan must be
		// dropped and the candidate (which carries match/art) promoted in its place.
		db := openTestDB(t)
		libID := seedLibrary(t, db, "Music", "music", "/tmp/music")
		ctx := context.Background()

		va := seedArtist(t, db, libID, "Various Artists")
		orphan := seedAlbum(t, db, libID, va, "Collide", 2000, true) // empty, is_compilation=1

		x := seedArtist(t, db, libID, "X")
		y := seedArtist(t, db, libID, "Y")
		cand := seedAlbum(t, db, libID, x, "Collide", 2000, false)
		seedTrack(t, db, libID, x, cand, "CX", 1, "/tmp/music/cx.mp3")
		seedTrack(t, db, libID, y, cand, "CY", 2, "/tmp/music/cy.mp3")
		if _, err := db.Exec("UPDATE music_albums SET art_path=?, match_status='matched' WHERE id=?", "/thumbs/cand.jpg", cand); err != nil {
			t.Fatalf("set art: %v", err)
		}

		scanner := &Scanner{DB: db}
		if err := scanner.finalizeCompilations(ctx, libID); err != nil {
			t.Fatalf("finalizeCompilations: %v", err)
		}

		// Exactly one album row survives for (Collide, 2000): the candidate.
		var id, artistID int64
		var isComp int
		var artPath string
		if err := db.QueryRow(
			"SELECT id, is_compilation, artist_id, art_path FROM music_albums WHERE library_id=? AND lower(title)=lower(?) AND year=?",
			libID, "Collide", 2000,
		).Scan(&id, &isComp, &artistID, &artPath); err != nil {
			t.Fatalf("query survivor (expected exactly one row): %v", err)
		}
		if id != cand {
			t.Fatalf("expected surviving album to be the candidate %d, got %d (orphan kept?)", cand, id)
		}
		if isComp != 1 || artistID != va {
			t.Fatalf("expected promoted VA compilation, got is_comp=%d artist=%d", isComp, artistID)
		}
		if artPath != "/thumbs/cand.jpg" {
			t.Fatalf("candidate art_path lost: %q", artPath)
		}
		var nTracks int
		db.QueryRow("SELECT COUNT(*) FROM music_tracks WHERE album_id=?", cand).Scan(&nTracks)
		if nTracks != 2 {
			t.Fatalf("expected 2 tracks on survivor, got %d", nTracks)
		}
		// Orphan row is gone.
		var orphanGone int
		db.QueryRow("SELECT COUNT(*) FROM music_albums WHERE id=?", orphan).Scan(&orphanGone)
		if orphanGone != 0 {
			t.Fatalf("empty VA orphan should have been deleted")
		}
	})

	t.Run("collision with non-empty Various Artists album merges into it", func(t *testing.T) {
		db := openTestDB(t)
		libID := seedLibrary(t, db, "Music", "music", "/tmp/music")
		ctx := context.Background()

		va := seedArtist(t, db, libID, "Various Artists")
		p := seedArtist(t, db, libID, "P")
		q := seedArtist(t, db, libID, "Q")
		existing := seedAlbum(t, db, libID, va, "Merge", 2001, true) // real comp, has tracks
		seedTrack(t, db, libID, p, existing, "EP", 1, "/tmp/music/ep.mp3")
		seedTrack(t, db, libID, q, existing, "EQ", 2, "/tmp/music/eq.mp3")

		x := seedArtist(t, db, libID, "RX")
		y := seedArtist(t, db, libID, "RY")
		cand := seedAlbum(t, db, libID, x, "Merge", 2001, false)
		seedTrack(t, db, libID, x, cand, "MX", 1, "/tmp/music/mx.mp3")
		seedTrack(t, db, libID, y, cand, "MY", 2, "/tmp/music/my.mp3")

		scanner := &Scanner{DB: db}
		if err := scanner.finalizeCompilations(ctx, libID); err != nil {
			t.Fatalf("finalizeCompilations: %v", err)
		}

		// All four tracks consolidate onto the existing VA album.
		var ids []int64
		rows, err := db.Query("SELECT DISTINCT album_id FROM music_tracks WHERE library_id=?", libID)
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				t.Fatalf("scan: %v", err)
			}
			ids = append(ids, id)
		}
		if len(ids) != 1 || ids[0] != existing {
			t.Fatalf("expected all tracks on existing VA album %d, got %v", existing, ids)
		}
		var n int
		db.QueryRow("SELECT COUNT(*) FROM music_tracks WHERE album_id=?", existing).Scan(&n)
		if n != 4 {
			t.Fatalf("expected 4 tracks merged, got %d", n)
		}
	})
}

// TestFinalizeCompilationsDirectoryScope pins the co-location rule on the
// variant merge: only albums whose files live in the compilation's own
// folder(s) are absorbed -- a genuinely distinct album that merely shares
// the title+year survives.
func TestFinalizeCompilationsDirectoryScope(t *testing.T) {
	t.Run("distinct same-title+year album in another folder is not absorbed", func(t *testing.T) {
		db := openTestDB(t)
		libID := seedLibrary(t, db, "Music", "music", "/tmp/music")
		ctx := context.Background()

		// Untagged VA compilation "Live" (2015) in /tmp/music/comp: one
		// multi-artist candidate row plus a per-artist fragment row.
		a1 := seedArtist(t, db, libID, "Artist One")
		a2 := seedArtist(t, db, libID, "Artist Two")
		a3 := seedArtist(t, db, libID, "Artist Three")
		cand := seedAlbum(t, db, libID, a1, "Live", 2015, false)
		seedTrack(t, db, libID, a1, cand, "Song A", 1, "/tmp/music/comp/a.mp3")
		seedTrack(t, db, libID, a2, cand, "Song B", 2, "/tmp/music/comp/b.mp3")
		frag := seedAlbum(t, db, libID, a3, "Live", 2015, false)
		seedTrack(t, db, libID, a3, frag, "Song C", 3, "/tmp/music/comp/c.mp3")

		// Artist X's own studio album "Live" (2015) in its own folder.
		ax := seedArtist(t, db, libID, "Artist X")
		own := seedAlbum(t, db, libID, ax, "Live", 2015, false)
		seedTrack(t, db, libID, ax, own, "Solo 1", 1, "/tmp/music/artistx/live/s1.mp3")
		seedTrack(t, db, libID, ax, own, "Solo 2", 2, "/tmp/music/artistx/live/s2.mp3")

		scanner := &Scanner{DB: db}
		if err := scanner.finalizeCompilations(ctx, libID); err != nil {
			t.Fatalf("finalizeCompilations: %v", err)
		}

		// The co-located fragment merged onto the candidate...
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM music_tracks WHERE album_id=?", cand).Scan(&n); err != nil {
			t.Fatalf("query candidate tracks: %v", err)
		}
		if n != 3 {
			t.Fatalf("expected 3 tracks on the compilation, got %d", n)
		}
		// ...while artist X's album keeps its row and both tracks.
		if err := db.QueryRow("SELECT COUNT(*) FROM music_tracks WHERE album_id=?", own).Scan(&n); err != nil {
			t.Fatalf("query own-album tracks: %v", err)
		}
		if n != 2 {
			t.Fatalf("expected artist X's album to keep 2 tracks, got %d", n)
		}
		var isComp int
		if err := db.QueryRow("SELECT is_compilation FROM music_albums WHERE id=?", own).Scan(&isComp); err != nil {
			t.Fatalf("artist X's album row was absorbed/deleted: %v", err)
		}
		if isComp != 0 {
			t.Fatalf("artist X's album wrongly marked compilation")
		}
	})

	t.Run("disc subfolders count as the same folder", func(t *testing.T) {
		db := openTestDB(t)
		libID := seedLibrary(t, db, "Music", "music", "/tmp/music")
		ctx := context.Background()

		// Multi-disc compilation: candidate tracks on Disc 1, a fragment
		// variant on Disc 2 -- both normalize to the comp folder.
		a1 := seedArtist(t, db, libID, "Artist One")
		a2 := seedArtist(t, db, libID, "Artist Two")
		a3 := seedArtist(t, db, libID, "Artist Three")
		cand := seedAlbum(t, db, libID, a1, "Box Set", 2010, false)
		seedTrack(t, db, libID, a1, cand, "Song A", 1, "/tmp/music/boxset/Disc 1/a.mp3")
		seedTrack(t, db, libID, a2, cand, "Song B", 2, "/tmp/music/boxset/Disc 1/b.mp3")
		frag := seedAlbum(t, db, libID, a3, "Box Set", 2010, false)
		seedTrack(t, db, libID, a3, frag, "Song C", 1, "/tmp/music/boxset/Disc 2/c.mp3")

		scanner := &Scanner{DB: db}
		if err := scanner.finalizeCompilations(ctx, libID); err != nil {
			t.Fatalf("finalizeCompilations: %v", err)
		}

		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM music_tracks WHERE album_id=?", cand).Scan(&n); err != nil {
			t.Fatalf("query: %v", err)
		}
		if n != 3 {
			t.Fatalf("expected the Disc 2 fragment merged (3 tracks), got %d", n)
		}
	})
}
