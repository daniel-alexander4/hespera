package integrity

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	isodb "hespera/internal/db"

	_ "modernc.org/sqlite"
)

// TestCheckLibraryAudioFlagLive proves the CHEAP tier (the one chained after a
// scan, so it runs on newly-added media) examines audio and flags a file with an
// audio gap. Gated on HESPERA_LIVE_FIXTURE (a file with an audio gap). Runs with
// repair OFF so it never writes to the fixture — read-only.
func TestCheckLibraryAudioFlagLive(t *testing.T) {
	fixture := os.Getenv("HESPERA_LIVE_FIXTURE")
	if fixture == "" {
		t.Skip("set HESPERA_LIVE_FIXTURE=<file with an audio gap> to run")
	}
	conn, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "t.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := isodb.Migrate(conn); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec("INSERT INTO libraries (id, name, type, root_path) VALUES (1,'test','tv',?)", filepath.Dir(fixture)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec("INSERT INTO scan_jobs (id, library_id, job_type, status, created_by) VALUES (1,1,'integrity_check','running','system')"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec("INSERT INTO tv_series_files (id, library_id, abs_path, integrity_status) VALUES (1,1,?,'')", fixture); err != nil {
		t.Fatal(err)
	}

	// repair=false → no file writes; the audio-gap examination must still flag.
	if err := CheckLibrary(context.Background(), conn, filepath.Dir(fixture), "tv_series_files", 1, 1, false); err != nil {
		t.Fatalf("CheckLibrary: %v", err)
	}
	var status, detail string
	if err := conn.QueryRow("SELECT integrity_status, integrity_detail FROM tv_series_files WHERE id=1").Scan(&status, &detail); err != nil {
		t.Fatal(err)
	}
	t.Logf("status=%q detail=%q", status, detail)
	if status != "flagged" || !strings.Contains(detail, "audio gap") {
		t.Fatalf("cheap tier should flag the audio gap: got status=%q detail=%q", status, detail)
	}
}

// TestCheckLibraryMusicFlagLive proves the cheap/scan-time tier flags a corrupt
// MUSIC file — for MP3 the corruption surfaces only on the full decode, which
// the music tier runs at scan time. Gated on HESPERA_LIVE_MUSIC_FIXTURE (a
// corrupt audio file). Repair off → read-only.
func TestCheckLibraryMusicFlagLive(t *testing.T) {
	fixture := os.Getenv("HESPERA_LIVE_MUSIC_FIXTURE")
	if fixture == "" {
		t.Skip("set HESPERA_LIVE_MUSIC_FIXTURE=<corrupt audio file> to run")
	}
	conn, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "m.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := isodb.Migrate(conn); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec("INSERT INTO libraries (id, name, type, root_path) VALUES (1,'m','music',?)", filepath.Dir(fixture)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec("INSERT INTO music_artists (id, library_id, name) VALUES (1,1,'a')"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec("INSERT INTO music_albums (id, library_id, artist_id, title) VALUES (1,1,1,'al')"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec("INSERT INTO scan_jobs (id, library_id, job_type, status, created_by) VALUES (1,1,'integrity_check','running','system')"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec("INSERT INTO music_tracks (id, library_id, artist_id, album_id, title, abs_path, integrity_status) VALUES (1,1,1,1,'t',?, '')", fixture); err != nil {
		t.Fatal(err)
	}
	if err := CheckLibrary(context.Background(), conn, filepath.Dir(fixture), "music_tracks", 1, 1, false); err != nil {
		t.Fatalf("CheckLibrary: %v", err)
	}
	var status, detail string
	if err := conn.QueryRow("SELECT integrity_status, integrity_detail FROM music_tracks WHERE id=1").Scan(&status, &detail); err != nil {
		t.Fatal(err)
	}
	t.Logf("status=%q detail=%q", status, detail)
	if status != "flagged" || !strings.Contains(detail, "bitstream corruption") {
		t.Fatalf("music tier should flag the bitstream corruption: got status=%q detail=%q", status, detail)
	}
}
