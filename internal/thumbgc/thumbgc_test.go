package thumbgc

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	isodb "hespera/internal/db"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	conn, err := isodb.Open(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := isodb.Migrate(conn); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return conn
}

func writeFile(t *testing.T, path string, age time.Duration) {
	t.Helper()
	if err := os.WriteFile(path, []byte("img"), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if age > 0 {
		mod := time.Now().Add(-age)
		if err := os.Chtimes(path, mod, mod); err != nil {
			t.Fatalf("chtimes %s: %v", path, err)
		}
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestSweepMusic(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)

	dataDir := t.TempDir()
	musicDir := filepath.Join(dataDir, "thumbs", "music")
	if err := os.MkdirAll(filepath.Join(musicDir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}

	refAlbum := filepath.Join(musicDir, "album.jpg")
	refArtist := filepath.Join(musicDir, "artist.png")
	orphanOld := filepath.Join(musicDir, "orphan.jpg")
	orphanFresh := filepath.Join(musicDir, "fresh.jpg")
	nonArt := filepath.Join(musicDir, "notes.txt")
	nested := filepath.Join(musicDir, "sub", "deep.jpg")

	// Referenced files are deliberately OLD — reference must win over age.
	writeFile(t, refAlbum, time.Hour)
	writeFile(t, refArtist, time.Hour)
	writeFile(t, orphanOld, time.Hour) // unreferenced + old → deleted
	writeFile(t, orphanFresh, 0)       // unreferenced but within grace → kept
	writeFile(t, nonArt, time.Hour)    // wrong extension → kept
	writeFile(t, nested, time.Hour)    // in a subdir → kept (no recursion)

	// Seed the referencing rows (library → artist → album).
	res, err := db.Exec("INSERT INTO libraries (name, type, root_path) VALUES ('m','music','/m')")
	if err != nil {
		t.Fatal(err)
	}
	libID, _ := res.LastInsertId()
	res, err = db.Exec("INSERT INTO music_artists (library_id, name, art_path) VALUES (?, 'A', ?)", libID, refArtist)
	if err != nil {
		t.Fatal(err)
	}
	artistID, _ := res.LastInsertId()
	if _, err := db.Exec("INSERT INTO music_albums (library_id, artist_id, title, art_path) VALUES (?, ?, 'Alb', ?)", libID, artistID, refAlbum); err != nil {
		t.Fatal(err)
	}

	deleted, err := Sweep(ctx, db, musicDir, Grace,
		"SELECT art_path FROM music_albums WHERE art_path != ''",
		"SELECT art_path FROM music_artists WHERE art_path != ''",
	)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1", deleted)
	}

	if exists(orphanOld) {
		t.Error("orphanOld should have been deleted")
	}
	for _, p := range []string{refAlbum, refArtist, orphanFresh, nonArt, nested} {
		if !exists(p) {
			t.Errorf("%s was deleted, should have been kept", filepath.Base(p))
		}
	}
}

func TestSweepMissingDir(t *testing.T) {
	db := openTestDB(t)
	n, err := Sweep(context.Background(), db, filepath.Join(t.TempDir(), "does-not-exist"), Grace,
		"SELECT art_path FROM tv_series_art WHERE art_path != ''")
	if err != nil {
		t.Fatalf("missing dir should be a no-op, got err: %v", err)
	}
	if n != 0 {
		t.Fatalf("deleted = %d, want 0", n)
	}
}
