package scan

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"hespera/internal/config"
)

// --- synthetic ID3v2.3 builders (test-only; mirrors internal/music's) ---

func id3v23File(t *testing.T, path, title, artist, album string) {
	t.Helper()
	frame := func(id, val string) []byte {
		payload := append([]byte{0}, []byte(val)...) // enc 0 = ISO-8859-1
		sz := len(payload)
		hdr := append([]byte(id), byte(sz>>24), byte(sz>>16), byte(sz>>8), byte(sz), 0, 0)
		return append(hdr, payload...)
	}
	var body []byte
	body = append(body, frame("TIT2", title)...)
	body = append(body, frame("TPE1", artist)...)
	body = append(body, frame("TALB", album)...)
	sz := len(body)
	blob := append([]byte{'I', 'D', '3', 3, 0, 0,
		byte((sz >> 21) & 0x7F), byte((sz >> 14) & 0x7F), byte((sz >> 7) & 0x7F), byte(sz & 0x7F)}, body...)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, blob, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestScanMusicUnchangedFastPath: a library walk must not re-read (or re-write)
// a file whose size+mtime match its row — the per-file tag read + upsert
// transaction on every unchanged file is what made a no-op rescan crawl through
// the whole library. The targeted ScanFiles path (album Rescan button, tag
// editor) deliberately keeps the full read.
func TestScanMusicUnchangedFastPath(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	root := t.TempDir()
	libID := seedLibrary(t, db, "music", "music", root)
	s := New(config.Config{MediaRoot: root, DataDir: t.TempDir()}, db)

	path := filepath.Join(root, "Artist", "Album", "song.mp3")
	id3v23File(t, path, "Song A", "Artist", "Album")

	if err := s.ScanMusic(ctx, 0, libID); err != nil {
		t.Fatalf("initial ScanMusic: %v", err)
	}
	var title string
	if err := db.QueryRow("SELECT title FROM music_tracks WHERE library_id=? AND abs_path=?", libID, path).Scan(&title); err != nil {
		t.Fatalf("track not ingested: %v", err)
	}
	if title != "Song A" {
		t.Fatalf("initial scan: title=%q, want Song A", title)
	}

	// Sentinel the row; an unchanged file must be skipped entirely, so the
	// sentinel survives a rescan.
	if _, err := db.Exec("UPDATE music_tracks SET title='Sentinel' WHERE library_id=? AND abs_path=?", libID, path); err != nil {
		t.Fatal(err)
	}
	if err := s.ScanMusic(ctx, 0, libID); err != nil {
		t.Fatalf("rescan: %v", err)
	}
	_ = db.QueryRow("SELECT title FROM music_tracks WHERE library_id=? AND abs_path=?", libID, path).Scan(&title)
	if title != "Sentinel" {
		t.Fatalf("unchanged file was re-read on rescan (title=%q) — fast path not taken", title)
	}

	// Targeted rescan (the album Rescan / tag-editor path) must force the full
	// read even though the file is unchanged.
	if err := s.ScanFiles(ctx, libID, []string{path}); err != nil {
		t.Fatalf("ScanFiles: %v", err)
	}
	_ = db.QueryRow("SELECT title FROM music_tracks WHERE library_id=? AND abs_path=?", libID, path).Scan(&title)
	if title != "Song A" {
		t.Fatalf("ScanFiles skipped an unchanged file (title=%q) — targeted rescans must always read", title)
	}

	// A changed file (new mtime) must take the full path on a library walk.
	if _, err := db.Exec("UPDATE music_tracks SET title='Sentinel' WHERE library_id=? AND abs_path=?", libID, path); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, time.Now().Add(time.Hour), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.ScanMusic(ctx, 0, libID); err != nil {
		t.Fatalf("rescan after touch: %v", err)
	}
	_ = db.QueryRow("SELECT title FROM music_tracks WHERE library_id=? AND abs_path=?", libID, path).Scan(&title)
	if title != "Song A" {
		t.Fatalf("changed file not re-read (title=%q)", title)
	}
}
