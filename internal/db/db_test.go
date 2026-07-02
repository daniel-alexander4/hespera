package db

import (
	"path/filepath"
	"strings"
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
		"scan_jobs",
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

func TestEnsureColumnValidation(t *testing.T) {
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

	tests := []struct {
		name    string
		table   string
		col     string
		decl    string
		wantErr string // empty means expect nil error
	}{
		{
			name:    "valid_table_and_column",
			table:   "libraries",
			col:     "test_col",
			decl:    "TEXT",
			wantErr: "",
		},
		{
			name:    "valid_underscored_names",
			table:   "my_table",
			col:     "_col_2",
			decl:    "TEXT",
			wantErr: "", // table doesn't exist but validation should pass; SQL error is fine
		},
		{
			name:    "reject_table_with_space",
			table:   "my table",
			col:     "col",
			decl:    "TEXT",
			wantErr: "invalid table name",
		},
		{
			name:    "reject_table_with_semicolon",
			table:   "foo;DROP",
			col:     "col",
			decl:    "TEXT",
			wantErr: "invalid table name",
		},
		{
			name:    "reject_table_with_parens",
			table:   "foo()",
			col:     "col",
			decl:    "TEXT",
			wantErr: "invalid table name",
		},
		{
			name:    "reject_column_with_dash",
			table:   "libraries",
			col:     "my-col",
			decl:    "TEXT",
			wantErr: "invalid column name",
		},
		{
			name:    "reject_column_with_quotes",
			table:   "libraries",
			col:     "col'bad",
			decl:    "TEXT",
			wantErr: "invalid column name",
		},
		{
			name:    "reject_empty_table",
			table:   "",
			col:     "col",
			decl:    "TEXT",
			wantErr: "invalid table name",
		},
		{
			name:    "reject_empty_column",
			table:   "libraries",
			col:     "",
			decl:    "TEXT",
			wantErr: "invalid column name",
		},
		{
			name:    "reject_table_starting_with_digit",
			table:   "1table",
			col:     "col",
			decl:    "TEXT",
			wantErr: "invalid table name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ensureColumn(conn, tt.table, tt.col, tt.decl)
			if tt.wantErr == "" {
				if err != nil {
					// Allow SQL errors for valid identifiers on non-existent tables
					if strings.Contains(err.Error(), "invalid table name") || strings.Contains(err.Error(), "invalid column name") {
						t.Fatalf("ensureColumn rejected valid identifier: %v", err)
					}
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got: %v", tt.wantErr, err)
			}
		})
	}
}
