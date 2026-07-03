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
