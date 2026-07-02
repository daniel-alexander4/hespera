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
