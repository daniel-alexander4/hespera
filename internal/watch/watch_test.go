package watch

import (
	"database/sql"
	"os"
	"path/filepath"
	"sync/atomic"
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

func seedLib(t *testing.T, db *sql.DB, root string) int64 {
	t.Helper()
	res, err := db.Exec("INSERT INTO libraries (name, type, root_path) VALUES ('W', 'music', ?)", root)
	if err != nil {
		t.Fatalf("seed library: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

// start builds a Service with test-fast debounce and a counting enqueue.
func start(t *testing.T, db *sql.DB) (*Service, *atomic.Int64) {
	t.Helper()
	var fired atomic.Int64
	s, err := New(db, func(int64) { fired.Add(1) }, 60*time.Millisecond, time.Hour)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, &fired
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestWatchDebouncesToOneScan(t *testing.T) {
	db := openTestDB(t)
	root := t.TempDir()
	seedLib(t, db, root)
	_, fired := start(t, db)

	// A burst of writes (a copy in progress) must yield exactly one fire.
	for i := 0; i < 5; i++ {
		if err := os.WriteFile(filepath.Join(root, "track.mp3"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		time.Sleep(15 * time.Millisecond)
	}
	waitFor(t, func() bool { return fired.Load() == 1 })
	time.Sleep(150 * time.Millisecond) // quiet period — no second fire
	if got := fired.Load(); got != 1 {
		t.Fatalf("burst fired %d times, want 1", got)
	}
}

func TestWatchSeesNewSubdirectories(t *testing.T) {
	db := openTestDB(t)
	root := t.TempDir()
	seedLib(t, db, root)
	_, fired := start(t, db)

	// New album dir, then a file inside it — the dir Create must extend the
	// watch set so the inner write still triggers.
	sub := filepath.Join(root, "New Album")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return fired.Load() >= 1 }) // the mkdir itself fires once
	base := fired.Load()
	if err := os.WriteFile(filepath.Join(sub, "song.mp3"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return fired.Load() > base })
}

func TestWatchSkipsWhenScanActiveAndWhenDisabled(t *testing.T) {
	db := openTestDB(t)
	root := t.TempDir()
	libID := seedLib(t, db, root)
	_, fired := start(t, db)

	// An already-queued scan suppresses the fire (the jobs service has no dedup).
	if _, err := db.Exec(
		"INSERT INTO scan_jobs (library_id, job_type, status, created_at) VALUES (?, 'music_scan', 'running', datetime('now'))",
		libID); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.mp3"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if got := fired.Load(); got != 0 {
		t.Fatalf("fired %d with a scan already running, want 0", got)
	}
	if _, err := db.Exec("UPDATE scan_jobs SET status='done'"); err != nil {
		t.Fatal(err)
	}

	// The kill-switch suppresses too, without a restart.
	if _, err := db.Exec("INSERT INTO app_settings (key, value) VALUES ('watch_enabled', '0')"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.mp3"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if got := fired.Load(); got != 0 {
		t.Fatalf("fired %d while disabled, want 0", got)
	}
}

func TestWatchIgnoresHiddenPaths(t *testing.T) {
	db := openTestDB(t)
	root := t.TempDir()
	seedLib(t, db, root)
	_, fired := start(t, db)

	if err := os.WriteFile(filepath.Join(root, ".syncthing.tmp"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if got := fired.Load(); got != 0 {
		t.Fatalf("hidden file fired %d, want 0", got)
	}
}

func TestWatchPicksUpRuntimeAddedLibrary(t *testing.T) {
	db := openTestDB(t)
	var fired atomic.Int64
	s, err := New(db, func(int64) { fired.Add(1) }, 40*time.Millisecond, 60*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	// A library created after startup (with pre-existing content that will
	// never emit events) gets watched AND fired once by the next re-sync.
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "existing.mp3"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	seedLib(t, db, root)
	waitFor(t, func() bool { return fired.Load() == 1 })

	// And future events in it trigger normally.
	if err := os.WriteFile(filepath.Join(root, "new.mp3"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return fired.Load() >= 2 })
}

func TestWatchLibraryUnderDottedParent(t *testing.T) {
	// The hidden-path filter must apply below the root only — a library that
	// legitimately lives under a dotted parent (…/.parent/media) still fires.
	db := openTestDB(t)
	parent := filepath.Join(t.TempDir(), ".parent")
	root := filepath.Join(parent, "media")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	seedLib(t, db, root)
	_, fired := start(t, db)

	if err := os.WriteFile(filepath.Join(root, "track.mp3"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return fired.Load() == 1 })
}

// Boot reconcile: content added to a previously-scanned library WHILE THE APP
// WAS DOWN emits no events, so startup must detect it by directory mtime.
func TestBootReconcileScansChangedLibrary(t *testing.T) {
	db := openTestDB(t)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "New Show"), 0o755); err != nil {
		t.Fatal(err)
	}
	id := seedLib(t, db, root)
	// The last scan completed an hour ago; the root + show dir were created just
	// now (mtime = now), so their mtime is newer → boot must fire a rescan.
	if _, err := db.Exec(
		"INSERT INTO scan_jobs (library_id, job_type, status, ended_at) VALUES (?, 'music_scan', 'done', datetime('now','-1 hour'))",
		id); err != nil {
		t.Fatal(err)
	}
	_, fired := start(t, db) // New runs the boot reconcile
	waitFor(t, func() bool { return fired.Load() >= 1 })
}

func TestBootReconcileSkipsUnchangedAndUnscanned(t *testing.T) {
	// (a) Unchanged: every directory mtime predates the last scan → no fire.
	db := openTestDB(t)
	root := t.TempDir()
	sub := filepath.Join(root, "Old Show")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-2 * time.Hour)
	for _, p := range []string{sub, root} { // set AFTER mkdir (which touched root)
		if err := os.Chtimes(p, past, past); err != nil {
			t.Fatal(err)
		}
	}
	id := seedLib(t, db, root)
	if _, err := db.Exec( // scan completed an hour ago — after the 2h-old dir mtimes
		"INSERT INTO scan_jobs (library_id, job_type, status, ended_at) VALUES (?, 'music_scan', 'done', datetime('now','-1 hour'))",
		id); err != nil {
		t.Fatal(err)
	}
	_, fired := start(t, db)
	time.Sleep(200 * time.Millisecond) // past the 60ms debounce
	if got := fired.Load(); got != 0 {
		t.Fatalf("unchanged library reconciled %d times at boot, want 0", got)
	}

	// (b) Never scanned (no completed scan job) → no boot fire: initial scanning
	// is the add-library / manual-Scan path, not the reconcile's job.
	db2 := openTestDB(t)
	root2 := t.TempDir()
	if err := os.Mkdir(filepath.Join(root2, "Show"), 0o755); err != nil {
		t.Fatal(err)
	}
	seedLib(t, db2, root2)
	_, fired2 := start(t, db2)
	time.Sleep(200 * time.Millisecond)
	if got := fired2.Load(); got != 0 {
		t.Fatalf("never-scanned library fired %d times at boot, want 0", got)
	}
}
