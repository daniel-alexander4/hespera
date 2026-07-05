package db

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// queryPlan returns the concatenated EXPLAIN QUERY PLAN detail lines for a query,
// so a test can assert which index the planner chose.
func queryPlan(t *testing.T, dbPath, query string, args ...any) string {
	t.Helper()
	conn, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer conn.Close()
	rows, err := conn.Query("EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	var out []string
	for rows.Next() {
		cells := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range cells {
			ptrs[i] = &cells[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		for _, c := range cells {
			out = append(out, fmt.Sprint(c))
		}
	}
	return strings.Join(out, " ")
}

// TestScaleIndexesUsed pins that the scale indexes added for large-collection
// support are actually chosen by the planner — a new index on a populated table
// is worthless if the query plan ignores it.
func TestScaleIndexesUsed(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.sqlite")
	conn, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := Migrate(conn); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	// Seed a library + a couple of movie rows so the planner has a table to plan
	// against (empty tables can degenerate to a scan regardless of indexes).
	if _, err := conn.Exec("INSERT INTO libraries (name, type, root_path) VALUES ('m','movies','/m')"); err != nil {
		t.Fatalf("seed lib: %v", err)
	}
	// Production shape: a large matched-movie table, one flagged row, and a person
	// with only a few credits — so the planner should drive the filmography join
	// from credits (few rows) and probe movie_files by tmdb_id, and the flagged
	// count should touch just the one flagged row.
	for i := 0; i < 300; i++ {
		integrity := ""
		if i == 0 {
			integrity = "flagged"
		}
		if _, err := conn.Exec(
			"INSERT INTO movie_files (library_id, abs_path, tmdb_id, match_status, integrity_status) VALUES (1, ?, ?, 'matched', ?)",
			fmt.Sprintf("/m/%d.mkv", i), 1000+i, integrity,
		); err != nil {
			t.Fatalf("seed movie: %v", err)
		}
	}
	for i := 0; i < 3; i++ {
		if _, err := conn.Exec(
			"INSERT INTO credits (person_id, media_type, media_id) VALUES (1, 'movie', ?)", 1000+i,
		); err != nil {
			t.Fatalf("seed credit: %v", err)
		}
	}
	// Give the planner statistics so it costs the two join orders correctly.
	if _, err := conn.Exec("ANALYZE"); err != nil {
		t.Fatalf("analyze: %v", err)
	}
	conn.Close()

	// The Libraries-page flagged-count scan must use the partial flagged index.
	plan := queryPlan(t, dbPath, "SELECT library_id, COUNT(*) FROM movie_files WHERE integrity_status='flagged' GROUP BY library_id")
	if !strings.Contains(plan, "idx_movie_files_flagged") {
		t.Errorf("flagged-count query does not use idx_movie_files_flagged; plan: %s", plan)
	}

	// The actor-filmography join must use the tmdb_id index on the movie_files side.
	plan = queryPlan(t, dbPath,
		"SELECT DISTINCT mf.tmdb_id FROM credits c JOIN movie_files mf ON mf.tmdb_id = c.media_id WHERE c.person_id=? AND c.media_type='movie' AND mf.match_status='matched'", 1)
	if !strings.Contains(plan, "idx_movie_files_tmdb_id") {
		t.Errorf("filmography join does not use idx_movie_files_tmdb_id; plan: %s", plan)
	}
}

// TestPlayHistoryRecentlyPlayedIndex pins that the recently-played aggregate
// (rendered on the music home + landing pages) uses the composite index rather
// than a full-table scan whose cost grows with lifetime plays.
func TestPlayHistoryRecentlyPlayedIndex(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.sqlite")
	conn, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := Migrate(conn); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	// Seed a music library with many artists (each an aggregate group) and a lot of
	// play rows spread across them — the shape where a grouped-MAX full scan hurts.
	if _, err := conn.Exec("INSERT INTO libraries (name, type, root_path) VALUES ('m','music','/m')"); err != nil {
		t.Fatalf("seed lib: %v", err)
	}
	// One album + track per artist to satisfy play_history's FKs; the aggregate
	// groups by artist_id, so distinct artists are what the index must resolve.
	const artists = 40
	for a := 1; a <= artists; a++ {
		if _, err := conn.Exec("INSERT INTO music_artists (library_id, name) VALUES (1, ?)", fmt.Sprintf("artist %d", a)); err != nil {
			t.Fatalf("seed artist: %v", err)
		}
		if _, err := conn.Exec("INSERT INTO music_albums (library_id, artist_id, title) VALUES (1, ?, ?)", a, fmt.Sprintf("album %d", a)); err != nil {
			t.Fatalf("seed album: %v", err)
		}
		if _, err := conn.Exec("INSERT INTO music_tracks (library_id, artist_id, album_id, title, abs_path) VALUES (1, ?, ?, ?, ?)", a, a, fmt.Sprintf("track %d", a), fmt.Sprintf("/m/%d.flac", a)); err != nil {
			t.Fatalf("seed track: %v", err)
		}
		for p := 0; p < 25; p++ { // 1000 play rows total — enough that a scan is costed above the seek
			if _, err := conn.Exec("INSERT INTO play_history (track_id, library_id, artist_id, album_id, completed) VALUES (?, 1, ?, ?, 1)", a, a, a); err != nil {
				t.Fatalf("seed play: %v", err)
			}
		}
	}
	if _, err := conn.Exec("ANALYZE"); err != nil {
		t.Fatalf("analyze: %v", err)
	}
	conn.Close()

	// loadRecentlyPlayedArtists' inner aggregate (handlers_music.go).
	plan := queryPlan(t, dbPath, "SELECT artist_id, MAX(created_at) AS last_played FROM play_history WHERE library_id=? GROUP BY artist_id", 1)
	if !strings.Contains(plan, "idx_play_history_lib_artist_created") {
		t.Errorf("recently-played aggregate does not use idx_play_history_lib_artist_created; plan: %s", plan)
	}
}
