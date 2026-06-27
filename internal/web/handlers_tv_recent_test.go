package web

import (
	"context"
	"testing"
)

// Covers the TV "Recent" sub-tab loaders: both the recently-watched and
// recently-added queries should resolve a matched series to a display row, and
// an empty library should yield nil without error.
func TestRecentTVSeries(t *testing.T) {
	h, db := newTestHandler(t)
	ctx := context.Background()

	// Empty DB → nil, no error.
	if rows, err := h.recentTVSeries(ctx, tvRecentlyWatchedQuery, 18); err != nil || rows != nil {
		t.Fatalf("empty: rows=%v err=%v, want nil,nil", rows, err)
	}

	// Seed a matched series with one file and a playback-progress row.
	res, err := db.Exec("INSERT INTO libraries (name, type, root_path) VALUES ('TV', 'tv', '/tv')")
	if err != nil {
		t.Fatal(err)
	}
	libID, _ := res.LastInsertId()
	res, err = db.Exec(
		"INSERT INTO tv_series_files (library_id, abs_path, container, mtime_unix) VALUES (?, '/tv/show/s01e01.mkv', 'mkv', 1700000000)", libID)
	if err != nil {
		t.Fatal(err)
	}
	fileID, _ := res.LastInsertId()
	if _, err := db.Exec(
		`INSERT INTO tv_series_identities (file_id, status, provider, series_id, season_number, episode_numbers_csv)
		 VALUES (?, 'matched', 'tmdb', '999', 1, '1')`, fileID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		"INSERT INTO tv_playback_progress (file_id, position_seconds, duration_seconds, updated_at) VALUES (?, 120, 1400, datetime('now'))",
		fileID); err != nil {
		t.Fatal(err)
	}
	seedTVMetadata(t, db, "999")

	for _, c := range []struct{ name, query string }{
		{"recently watched", tvRecentlyWatchedQuery},
		{"recently added", tvRecentlyAddedQuery},
	} {
		rows, err := h.recentTVSeries(ctx, c.query, 18)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if len(rows) != 1 {
			t.Fatalf("%s: got %d rows, want 1", c.name, len(rows))
		}
		if rows[0].SeriesID != "999" || rows[0].Name != "Test Show" {
			t.Fatalf("%s: got %+v, want SeriesID=999 Name='Test Show'", c.name, rows[0])
		}
	}
}
