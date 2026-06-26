package scan

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	isodb "hespera/internal/db"

	"hespera/internal/config"
)

// openTestDB creates a temp SQLite database with migrations applied.
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

// seedLibrary inserts a library row and returns its ID.
func seedLibrary(t *testing.T, db *sql.DB, name, libType, rootPath string) int64 {
	t.Helper()
	res, err := db.Exec(
		"INSERT INTO libraries (name, type, root_path) VALUES (?, ?, ?)",
		name, libType, rootPath,
	)
	if err != nil {
		t.Fatalf("seedLibrary: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

// writeMinimalMP3 writes a minimal valid MP3 file with ID3v2.3 headers.
func writeMinimalMP3(t *testing.T, path, artist, album, title string, track int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	var buf []byte

	// Build ID3v2.3 text frames
	frames := []struct {
		id   string
		text string
	}{
		{"TPE1", artist},
		{"TALB", album},
		{"TIT2", title},
		{"TRCK", fmt.Sprintf("%d", track)},
	}

	var frameData []byte
	for _, f := range frames {
		textBytes := []byte(f.text)
		frameSize := 1 + len(textBytes) // encoding byte + text
		frame := make([]byte, 10+frameSize)
		copy(frame[0:4], f.id)
		binary.BigEndian.PutUint32(frame[4:8], uint32(frameSize))
		frame[8] = 0 // flags
		frame[9] = 0
		frame[10] = 0 // encoding: ISO-8859-1
		copy(frame[11:], textBytes)
		frameData = append(frameData, frame...)
	}

	// ID3v2.3 header: "ID3" + version 0x03 0x00 + flags 0x00 + syncsafe size
	tagSize := len(frameData)
	header := []byte{
		'I', 'D', '3',
		0x03, 0x00, // version 2.3
		0x00, // flags
		byte((tagSize >> 21) & 0x7F),
		byte((tagSize >> 14) & 0x7F),
		byte((tagSize >> 7) & 0x7F),
		byte(tagSize & 0x7F),
	}

	buf = append(buf, header...)
	buf = append(buf, frameData...)

	// Minimal MPEG audio frame (sync bytes so dhowden/tag recognizes it as MP3)
	mpegFrame := make([]byte, 20)
	mpegFrame[0] = 0xFF
	mpegFrame[1] = 0xFB
	mpegFrame[2] = 0x90
	mpegFrame[3] = 0x00
	buf = append(buf, mpegFrame...)

	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func TestEnsureArtist(t *testing.T) {
	db := openTestDB(t)
	libID := seedLibrary(t, db, "Test Music", "music", "/tmp/music")
	libID2 := seedLibrary(t, db, "Other Music", "music", "/tmp/other")
	ctx := context.Background()

	t.Run("new artist", func(t *testing.T) {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()

		id, err := ensureArtist(ctx, tx, libID, "Test Artist")
		if err != nil {
			t.Fatalf("ensureArtist: %v", err)
		}
		if id <= 0 {
			t.Fatalf("expected positive ID, got %d", id)
		}
		tx.Commit()
	})

	t.Run("idempotent", func(t *testing.T) {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()

		id1, err := ensureArtist(ctx, tx, libID, "Dupe Artist")
		if err != nil {
			t.Fatalf("first call: %v", err)
		}
		id2, err := ensureArtist(ctx, tx, libID, "Dupe Artist")
		if err != nil {
			t.Fatalf("second call: %v", err)
		}
		if id1 != id2 {
			t.Fatalf("expected same ID, got %d and %d", id1, id2)
		}
		tx.Commit()
	})

	t.Run("empty name defaults", func(t *testing.T) {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()

		id, err := ensureArtist(ctx, tx, libID, "")
		if err != nil {
			t.Fatalf("ensureArtist: %v", err)
		}
		if id <= 0 {
			t.Fatalf("expected positive ID, got %d", id)
		}
		var name string
		if err := tx.QueryRow("SELECT name FROM music_artists WHERE id=?", id).Scan(&name); err != nil {
			t.Fatalf("query name: %v", err)
		}
		if name != "Unknown Artist" {
			t.Fatalf("expected 'Unknown Artist', got %q", name)
		}
		tx.Commit()
	})

	t.Run("different library isolation", func(t *testing.T) {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()

		id1, err := ensureArtist(ctx, tx, libID, "Shared Name")
		if err != nil {
			t.Fatalf("lib1: %v", err)
		}
		id2, err := ensureArtist(ctx, tx, libID2, "Shared Name")
		if err != nil {
			t.Fatalf("lib2: %v", err)
		}
		if id1 == id2 {
			t.Fatalf("expected different IDs for different libraries, both got %d", id1)
		}
		tx.Commit()
	})
}

func TestEnsureAlbum(t *testing.T) {
	db := openTestDB(t)
	libID := seedLibrary(t, db, "Test Music", "music", "/tmp/music")
	ctx := context.Background()

	t.Run("new album", func(t *testing.T) {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()

		artistID, albumID, err := ensureAlbum(ctx, tx, libID, "The Beatles", "Abbey Road", 1969, false)
		if err != nil {
			t.Fatalf("ensureAlbum: %v", err)
		}
		if artistID <= 0 {
			t.Fatalf("expected positive artistID, got %d", artistID)
		}
		if albumID <= 0 {
			t.Fatalf("expected positive albumID, got %d", albumID)
		}
		tx.Commit()
	})

	t.Run("upsert on conflict", func(t *testing.T) {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()

		_, albumID1, err := ensureAlbum(ctx, tx, libID, "Pink Floyd", "The Wall", 1979, false)
		if err != nil {
			t.Fatalf("first call: %v", err)
		}
		_, albumID2, err := ensureAlbum(ctx, tx, libID, "Pink Floyd", "The Wall", 1979, false)
		if err != nil {
			t.Fatalf("second call: %v", err)
		}
		if albumID1 != albumID2 {
			t.Fatalf("expected same albumID, got %d and %d", albumID1, albumID2)
		}
		tx.Commit()
	})

	t.Run("different year", func(t *testing.T) {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()

		_, albumID1, err := ensureAlbum(ctx, tx, libID, "Radiohead", "Kid A", 2000, false)
		if err != nil {
			t.Fatalf("year 2000: %v", err)
		}
		_, albumID2, err := ensureAlbum(ctx, tx, libID, "Radiohead", "Kid A", 2001, false)
		if err != nil {
			t.Fatalf("year 2001: %v", err)
		}
		if albumID1 == albumID2 {
			t.Fatalf("expected different albumIDs for different years, both got %d", albumID1)
		}
		tx.Commit()
	})
}

func TestScanFile(t *testing.T) {
	db := openTestDB(t)
	mediaRoot := t.TempDir()
	thumbDir := t.TempDir()
	libID := seedLibrary(t, db, "Test Music", "music", mediaRoot)

	cfg := config.Config{
		MediaRoot: mediaRoot,
		DataDir:   t.TempDir(),
	}
	scanner := New(cfg, db)
	ctx := context.Background()

	t.Run("creates track from mp3 fixture", func(t *testing.T) {
		mp3Path := filepath.Join(mediaRoot, "artist", "album", "track01.mp3")
		writeMinimalMP3(t, mp3Path, "Test Artist", "Test Album", "Test Track", 1)

		if err := scanner.ScanFile(ctx, libID, mp3Path, thumbDir); err != nil {
			t.Fatalf("ScanFile: %v", err)
		}

		// Verify artist row
		var artistName string
		if err := db.QueryRow("SELECT name FROM music_artists WHERE library_id=? AND name=?", libID, "Test Artist").Scan(&artistName); err != nil {
			t.Fatalf("artist not found: %v", err)
		}

		// Verify album row
		var albumTitle string
		if err := db.QueryRow("SELECT title FROM music_albums WHERE library_id=?", libID).Scan(&albumTitle); err != nil {
			t.Fatalf("album not found: %v", err)
		}
		if albumTitle != "Test Album" {
			t.Fatalf("expected album 'Test Album', got %q", albumTitle)
		}

		// Verify track row
		var trackTitle string
		if err := db.QueryRow("SELECT title FROM music_tracks WHERE library_id=?", libID).Scan(&trackTitle); err != nil {
			t.Fatalf("track not found: %v", err)
		}
		if trackTitle != "Test Track" {
			t.Fatalf("expected track 'Test Track', got %q", trackTitle)
		}
	})

	t.Run("idempotent rescan", func(t *testing.T) {
		mp3Path := filepath.Join(mediaRoot, "artist2", "album2", "track01.mp3")
		writeMinimalMP3(t, mp3Path, "Artist2", "Album2", "Track2", 1)

		if err := scanner.ScanFile(ctx, libID, mp3Path, thumbDir); err != nil {
			t.Fatalf("first scan: %v", err)
		}
		if err := scanner.ScanFile(ctx, libID, mp3Path, thumbDir); err != nil {
			t.Fatalf("second scan: %v", err)
		}

		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM music_tracks WHERE abs_path=?", mp3Path).Scan(&count); err != nil {
			t.Fatalf("count: %v", err)
		}
		if count != 1 {
			t.Fatalf("expected 1 track row, got %d", count)
		}
	})

	t.Run("skips file outside media root", func(t *testing.T) {
		outsideDir := t.TempDir()
		mp3Path := filepath.Join(outsideDir, "outside.mp3")
		writeMinimalMP3(t, mp3Path, "Outside Artist", "Outside Album", "Outside Track", 1)

		// Count tracks before
		var before int
		db.QueryRow("SELECT COUNT(*) FROM music_tracks WHERE library_id=?", libID).Scan(&before)

		err := scanner.ScanFile(ctx, libID, mp3Path, thumbDir)
		if err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}

		var after int
		db.QueryRow("SELECT COUNT(*) FROM music_tracks WHERE library_id=?", libID).Scan(&after)
		if after != before {
			t.Fatalf("expected no new tracks, before=%d after=%d", before, after)
		}
	})
}
