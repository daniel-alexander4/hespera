package db

import (
	"path/filepath"
	"testing"
)

func openTestDB(t *testing.T) *testDBResult {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.sqlite")
	conn, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &testDBResult{DB: conn, Path: dbPath}
}

type testDBResult struct {
	DB   interface{ Ping() error }
	Path string
}

func TestOpenAndPing(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.sqlite")
	conn, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer conn.Close()

	if err := conn.Ping(); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

func TestMigrateIdempotent(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.sqlite")
	conn, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer conn.Close()

	// Run migrate twice — should be idempotent.
	if err := Migrate(conn); err != nil {
		t.Fatalf("Migrate (first): %v", err)
	}
	if err := Migrate(conn); err != nil {
		t.Fatalf("Migrate (second): %v", err)
	}

	// Verify tables exist.
	tables := []string{
		"libraries", "music_artists", "music_albums", "music_tracks",
		"play_history", "tv_series_files", "tv_series_identities",
		"scan_jobs", "auth_users", "auth_user_keys",
		"movie_files", "movie_metadata_cache", "movie_art", "movie_playback_progress",
	}
	for _, table := range tables {
		var count int
		err := conn.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count)
		if err != nil {
			t.Fatalf("table %s does not exist: %v", table, err)
		}
	}
}

func TestEnsureColumnIdempotent(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.sqlite")
	conn, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer conn.Close()

	if err := Migrate(conn); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// Ensure a column that already exists — should be a no-op.
	if err := ensureColumn(conn, "libraries", "name", "TEXT NOT NULL DEFAULT ''"); err != nil {
		t.Fatalf("ensureColumn existing: %v", err)
	}

	// Ensure a new column.
	if err := ensureColumn(conn, "libraries", "test_col", "TEXT NOT NULL DEFAULT 'hello'"); err != nil {
		t.Fatalf("ensureColumn new: %v", err)
	}

	// Ensure it again — idempotent.
	if err := ensureColumn(conn, "libraries", "test_col", "TEXT NOT NULL DEFAULT 'hello'"); err != nil {
		t.Fatalf("ensureColumn repeat: %v", err)
	}
}
