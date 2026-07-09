package web

import (
	"os"
	"path/filepath"
	"testing"

	"hespera/internal/config"
)

// TestResumeInterruptedJobsAtStartup verifies the boot auto-resume: a library
// whose jobs were interrupted by a restart gets its scan chain re-kicked
// (created_by='resume'), while libraries whose root is missing or empty at
// boot — the unmounted-mount-point hazard, where a scan would prune every
// row — are skipped.
func TestResumeInterruptedJobsAtStartup(t *testing.T) {
	dir := t.TempDir()
	db := openTestDB(t)

	good := filepath.Join(dir, "music-good")
	if err := os.MkdirAll(good, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(good, "note.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(dir, "music-empty")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "music-missing") // never created

	seedLib := func(name, root string) int64 {
		res, err := db.Exec("INSERT INTO libraries (name, type, root_path) VALUES (?, 'music', ?)", name, root)
		if err != nil {
			t.Fatalf("seed library %s: %v", name, err)
		}
		id, _ := res.LastInsertId()
		if _, err := db.Exec(
			"INSERT INTO scan_jobs (library_id, job_type, status, created_at) VALUES (?, 'music_match', 'running', datetime('now'))",
			id,
		); err != nil {
			t.Fatalf("seed stale job for %s: %v", name, err)
		}
		return id
	}
	goodID := seedLib("good", good)
	seedLib("empty", empty)
	seedLib("missing", missing)

	if _, err := New(Deps{
		Cfg:      config.Config{DataDir: dir, MediaRoot: dir},
		DB:       db,
		AssetsFS: stubAssetsFS(),
	}); err != nil {
		t.Fatalf("New: %v", err)
	}

	// The resume head job is inserted synchronously during New. Chained jobs are
	// created_by='system', so counting 'resume' rows isolates the boot re-kicks.
	rows, err := db.Query("SELECT library_id, job_type FROM scan_jobs WHERE created_by='resume'")
	if err != nil {
		t.Fatalf("query resume jobs: %v", err)
	}
	defer rows.Close()
	var got []struct {
		lib int64
		typ string
	}
	for rows.Next() {
		var r struct {
			lib int64
			typ string
		}
		if err := rows.Scan(&r.lib, &r.typ); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	if len(got) != 1 || got[0].lib != goodID || got[0].typ != "music_scan" {
		t.Fatalf("want exactly one resume head scan for library %d, got %+v", goodID, got)
	}
}

// TestResumeInterruptedJobsDisabled verifies the job_resume_enabled kill-switch:
// stored '0' suppresses the boot re-kick entirely.
func TestResumeInterruptedJobsDisabled(t *testing.T) {
	dir := t.TempDir()
	db := openTestDB(t)

	root := filepath.Join(dir, "music")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec("INSERT INTO libraries (name, type, root_path) VALUES ('m', 'music', ?)", root)
	if err != nil {
		t.Fatal(err)
	}
	libID, _ := res.LastInsertId()
	if _, err := db.Exec(
		"INSERT INTO scan_jobs (library_id, job_type, status, created_at) VALUES (?, 'music_scan', 'queued', datetime('now'))",
		libID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO app_settings (key, value) VALUES ('job_resume_enabled', '0')"); err != nil {
		t.Fatal(err)
	}

	if _, err := New(Deps{
		Cfg:      config.Config{DataDir: dir, MediaRoot: dir},
		DB:       db,
		AssetsFS: stubAssetsFS(),
	}); err != nil {
		t.Fatalf("New: %v", err)
	}

	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM scan_jobs WHERE created_by='resume'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("toggle off: want 0 resume jobs, got %d", n)
	}
}
