package db

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// migratedTestDB opens a fresh temp DB with the full schema applied.
func migratedTestDB(t *testing.T) *sql.DB {
	t.Helper()
	conn, err := Open(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := Migrate(conn); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return conn
}

// TestMigrateIntegrityDegraded pins the one-time reclassification: a flagged
// row whose only finding was an audio gap becomes 'degraded' (with the
// playable-residue suffix), while decode-error and repair-failed rows stay
// 'flagged'. Idempotent on re-run.
func TestMigrateIntegrityDegraded(t *testing.T) {
	db := migratedTestDB(t)
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("exec: %v", err)
		}
	}
	mustExec(`INSERT INTO libraries (name, type, root_path) VALUES ('tv','tv','/m/tv')`)
	seed := func(path, status, detail string) {
		mustExec(`INSERT INTO tv_series_files (library_id, abs_path, integrity_status, integrity_detail) VALUES (1, ?, ?, ?)`,
			path, status, detail)
	}
	// The real Doctor Who shape: repaired container + gap, previously flagged.
	seed("/m/tv/e2.mkv", "flagged", "container remuxed (6 errors dropped); audio gap 3.9s (missing audio)")
	seed("/m/tv/bad.mkv", "flagged", "bitstream corruption (4 decode errors); audio gap 2.0s (missing audio) — data loss, not auto-repairable")
	seed("/m/tv/failed.mkv", "flagged", "container corruption (repair failed verification); audio gap 1.0s (missing audio)")
	seed("/m/tv/ok.mkv", "repaired", "container remuxed (2 errors dropped)")

	if err := migrateIntegrityDegraded(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	get := func(path string) (status, detail string) {
		t.Helper()
		if err := db.QueryRow(`SELECT integrity_status, integrity_detail FROM tv_series_files WHERE abs_path=?`, path).
			Scan(&status, &detail); err != nil {
			t.Fatalf("query %s: %v", path, err)
		}
		return
	}
	if s, d := get("/m/tv/e2.mkv"); s != "degraded" || !strings.Contains(d, "silence-fills") {
		t.Fatalf("gap-only row: status=%q detail=%q, want degraded + suffix", s, d)
	}
	if s, _ := get("/m/tv/bad.mkv"); s != "flagged" {
		t.Fatalf("decode-error row downgraded to %q", s)
	}
	if s, _ := get("/m/tv/failed.mkv"); s != "flagged" {
		t.Fatalf("repair-failed row downgraded to %q", s)
	}
	if s, _ := get("/m/tv/ok.mkv"); s != "repaired" {
		t.Fatalf("repaired row changed to %q", s)
	}

	// Idempotent: re-running must not double-append the suffix.
	if err := migrateIntegrityDegraded(db); err != nil {
		t.Fatalf("re-migrate: %v", err)
	}
	if _, d := get("/m/tv/e2.mkv"); strings.Count(d, "silence-fills") != 1 {
		t.Fatalf("suffix duplicated on re-run: %q", d)
	}
}

// TestMigrateJobTypeRename renames legacy scan_jobs.job_type values to the
// consistent <media>_<action> scheme (idempotent on re-run).
func TestMigrateJobTypeRename(t *testing.T) {
	db := migratedTestDB(t)
	// Seed rows with the OLD names an upgraded DB would carry, then re-run
	// Migrate to exercise the rename.
	old := []string{"scan", "tvscan", "moviescan", "photoscan", "intro_detect", "tag_writeback", "tv_backdrop_refresh"}
	for _, jt := range old {
		if _, err := db.Exec(`INSERT INTO scan_jobs (library_id, job_type, status, created_at) VALUES (1, ?, 'done', '')`, jt); err != nil {
			t.Fatalf("seed %s: %v", jt, err)
		}
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("re-migrate: %v", err)
	}

	var stale int
	if err := db.QueryRow(`SELECT COUNT(*) FROM scan_jobs WHERE job_type IN
		('scan','tvscan','moviescan','photoscan','intro_detect','tag_writeback','tv_backdrop_refresh')`).Scan(&stale); err != nil {
		t.Fatal(err)
	}
	if stale != 0 {
		t.Fatalf("want 0 legacy job_type rows after migrate, got %d", stale)
	}
	for _, jt := range []string{"music_scan", "tv_scan", "movie_scan", "photo_scan", "tv_intro_detect", "music_tag_writeback", "tv_backdrop_fetch"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM scan_jobs WHERE job_type=?`, jt).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("want 1 %q row, got %d", jt, n)
		}
	}

	// Idempotent: a second re-run neither changes counts nor errors.
	if err := Migrate(db); err != nil {
		t.Fatalf("third migrate: %v", err)
	}
	var total int
	if err := db.QueryRow(`SELECT COUNT(*) FROM scan_jobs`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != len(old) {
		t.Fatalf("row count changed on re-run: want %d, got %d", len(old), total)
	}
}

// TestMigrateLibraryTypeHomeMedia proves the libraries-table rebuild renames
// 'photos' → 'home_media' (dropping the dead 'home_videos') while preserving row
// ids and child FKs, and enforces the new CHECK afterward.
func TestMigrateLibraryTypeHomeMedia(t *testing.T) {
	conn, err := Open(filepath.Join(t.TempDir(), "hm.sqlite"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	// The OLD schema an upgraded DB carries: the pre-rename CHECK + a child table
	// with an ON DELETE CASCADE FK; seed a 'photos' library with a child row.
	for _, s := range []string{
		`CREATE TABLE libraries (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL,
		   type TEXT NOT NULL CHECK(type IN ('music','movies','tv','photos','home_videos')),
		   root_path TEXT NOT NULL, created_at TEXT NOT NULL DEFAULT (datetime('now')))`,
		`CREATE TABLE photos (id INTEGER PRIMARY KEY, library_id INTEGER NOT NULL REFERENCES libraries(id) ON DELETE CASCADE)`,
		`INSERT INTO libraries (id, name, type, root_path) VALUES (7, 'Home', 'photos', '/m')`,
		`INSERT INTO photos (id, library_id) VALUES (100, 7)`,
	} {
		if _, err := conn.Exec(s); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}

	if err := migrateLibraryTypeHomeMedia(conn); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var typ string
	if err := conn.QueryRow("SELECT type FROM libraries WHERE id=7").Scan(&typ); err != nil {
		t.Fatalf("library gone / id not preserved: %v", err)
	}
	if typ != "home_media" {
		t.Fatalf("type=%q, want home_media", typ)
	}
	var libID int64
	if err := conn.QueryRow("SELECT library_id FROM photos WHERE id=100").Scan(&libID); err != nil || libID != 7 {
		t.Fatalf("child row lost its FK: library_id=%d err=%v", libID, err)
	}
	// No FK violation after the rebuild.
	rows, err := conn.Query("PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	if rows.Next() {
		rows.Close()
		t.Fatal("foreign_key_check reported a violation after migration")
	}
	rows.Close()
	// New CHECK enforced: 'photos' rejected, 'home_media' accepted.
	if _, err := conn.Exec("INSERT INTO libraries (name,type,root_path) VALUES ('x','photos','/x')"); err == nil {
		t.Fatal("old 'photos' type should now be rejected by the CHECK")
	}
	if _, err := conn.Exec("INSERT INTO libraries (name,type,root_path) VALUES ('y','home_media','/y')"); err != nil {
		t.Fatalf("home_media should be accepted: %v", err)
	}
	// Idempotent: a second run is a no-op and doesn't error.
	if err := migrateLibraryTypeHomeMedia(conn); err != nil {
		t.Fatalf("idempotent re-run: %v", err)
	}
}
