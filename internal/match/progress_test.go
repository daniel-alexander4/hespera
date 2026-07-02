package match

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
)

func readProgress(t *testing.T, db *sql.DB, jobID int64) (cur, total int) {
	t.Helper()
	if err := db.QueryRow("SELECT progress_current, progress_total FROM scan_jobs WHERE id=?", jobID).Scan(&cur, &total); err != nil {
		t.Fatalf("read progress: %v", err)
	}
	return
}

// TestProgressHelpers verifies the accumulating-progress primitives: each phase
// extends the total and continues the count from the prior phase's current.
func TestProgressHelpers(t *testing.T) {
	db := openTestDB(t)
	m := &Matcher{db: db}
	ctx := context.Background()
	res, err := db.Exec("INSERT INTO scan_jobs (library_id, job_type, status, created_by) VALUES (1,'music_match','running','user')")
	if err != nil {
		t.Fatal(err)
	}
	jobID, _ := res.LastInsertId()

	if base := m.progressAddTotal(ctx, jobID, 5); base != 0 {
		t.Fatalf("phase 1 base: want 0, got %d", base)
	}
	m.progressSet(ctx, jobID, 3)
	if base := m.progressAddTotal(ctx, jobID, 2); base != 3 {
		t.Fatalf("phase 2 base (continues from current): want 3, got %d", base)
	}
	if base := m.progressAddTotal(ctx, jobID, 0); base != 0 {
		t.Fatalf("n<=0 is a no-op returning 0, got %d", base)
	}
	if cur, total := readProgress(t, db, jobID); cur != 3 || total != 7 {
		t.Fatalf("want current=3 total=7, got %d/%d", cur, total)
	}
}

// TestEnrichArtistsReportsProgress proves the enrichment phase now sets a total
// and ticks the count — the whole point of the change. Artists are seeded
// already-complete so the loop skips every network call (m.mb stays nil), but
// the per-artist progress tick still runs.
func TestEnrichArtistsReportsProgress(t *testing.T) {
	db := openTestDB(t)
	m := &Matcher{db: db} // no MusicBrainz client — complete artists never call it
	ctx := context.Background()

	res, err := db.Exec("INSERT INTO libraries (name, type, root_path) VALUES ('M','music','/m')")
	if err != nil {
		t.Fatal(err)
	}
	libID, _ := res.LastInsertId()
	jres, err := db.Exec("INSERT INTO scan_jobs (library_id, job_type, status, created_by) VALUES (?,'music_match','running','user')", libID)
	if err != nil {
		t.Fatal(err)
	}
	jobID, _ := jres.LastInsertId()

	const n = 3
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("Artist %d", i)
		ares, err := db.Exec(
			"INSERT INTO music_artists (library_id, name, musicbrainz_id, bio, art_path) VALUES (?, ?, 'mbid', 'bio', '/art')",
			libID, name)
		if err != nil {
			t.Fatal(err)
		}
		artID, _ := ares.LastInsertId()
		if _, err := db.Exec(
			"INSERT INTO music_albums (library_id, artist_id, album_artist_id, title, year) VALUES (?, ?, ?, ?, 2000)",
			libID, artID, artID, "Alb "+name); err != nil {
			t.Fatal(err)
		}
	}

	if err := m.enrichArtists(ctx, jobID, libID); err != nil {
		t.Fatalf("enrichArtists: %v", err)
	}
	cur, total := readProgress(t, db, jobID)
	if total != n {
		t.Fatalf("progress_total: want %d, got %d", n, total)
	}
	if cur != n {
		t.Fatalf("progress_current: want %d (bar reaches 100%% by phase end), got %d", n, cur)
	}
}
