package web

import (
	"context"
	"fmt"
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

// TestContinueWatchingTargetSeason covers the roll-forward season the Continue
// Watching card deep-links to: stay on the in-progress season while it has an
// unwatched episode, advance to the next season with something to play once it's
// fully watched, and fall back to the last season when everything is watched.
func TestContinueWatchingTargetSeason(t *testing.T) {
	h, db := newTestHandler(t)
	ctx := context.Background()

	res, err := db.Exec("INSERT INTO libraries (name, type, root_path) VALUES ('TV', 'tv', '/tv')")
	if err != nil {
		t.Fatal(err)
	}
	libID, _ := res.LastInsertId()

	// addEp inserts a matched episode and its progress (completed flag), with a
	// watchedAt that orders "most recently watched". Returns nothing — the query
	// derives everything from the rows.
	var clock int
	addEp := func(seriesID string, season, episode int, completed bool, watched bool) {
		clock++
		res, err := db.Exec(
			"INSERT INTO tv_series_files (library_id, abs_path, container, mtime_unix) VALUES (?, ?, 'mkv', ?)",
			libID, fmt.Sprintf("/tv/%s/s%02de%02d.mkv", seriesID, season, episode), 1700000000+clock)
		if err != nil {
			t.Fatal(err)
		}
		fileID, _ := res.LastInsertId()
		if _, err := db.Exec(
			`INSERT INTO tv_series_identities (file_id, status, provider, series_id, season_number, episode_numbers_csv)
			 VALUES (?, 'matched', 'tmdb', ?, ?, ?)`, fileID, seriesID, season, episode); err != nil {
			t.Fatal(err)
		}
		if watched {
			c := 0
			if completed {
				c = 1
			}
			// updated_at increases with clock so the latest-added watch wins MAX().
			if _, err := db.Exec(
				"INSERT INTO tv_playback_progress (file_id, position_seconds, duration_seconds, completed, updated_at) VALUES (?, 100, 1000, ?, datetime('now', ?))",
				fileID, c, fmt.Sprintf("+%d seconds", clock)); err != nil {
				t.Fatal(err)
			}
		}
	}

	// Series A: S1 fully watched, S2 in progress → target S2 (last watch is S2E1).
	addEp("A", 1, 1, true, true)
	addEp("A", 1, 2, true, true)
	addEp("A", 2, 1, false, true) // most-recent watch for A, not completed
	addEp("A", 2, 2, false, false)
	seedTVMetadata(t, db, "A")

	// Series B: most-recent watch is S1E2 which IS completed and S1 is fully
	// watched; S2 has unwatched episodes → roll forward to S2.
	addEp("B", 1, 1, true, true)
	addEp("B", 1, 2, true, true) // most-recent watch for B, completed, S1 done
	addEp("B", 2, 1, false, false)
	seedTVMetadata(t, db, "B")

	// Series C: only season is fully watched → stay on it (re-watch fallback).
	addEp("C", 1, 1, true, true) // most-recent watch for C
	seedTVMetadata(t, db, "C")

	rows, err := h.recentTVSeries(ctx, tvRecentlyWatchedQuery, 18)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, r := range rows {
		got[r.SeriesID] = r.SeasonNumber
	}
	for _, c := range []struct {
		series string
		want   int
	}{
		{"A", 2}, // in-progress season
		{"B", 2}, // rolled forward past a finished S1
		{"C", 1}, // everything watched → stay
	} {
		if got[c.series] != c.want {
			t.Errorf("series %s: target season = %d, want %d (rows=%+v)", c.series, got[c.series], c.want, rows)
		}
	}
}
