package web

import (
	"context"
	"testing"
)

// TestContinueWatchingMerged verifies the home "Continue Watching" row merges
// in-progress TV and movies into one list ordered by most-recent activity — the
// movie watched "now" must sort ahead of the show watched an hour earlier.
func TestContinueWatchingMerged(t *testing.T) {
	h, db := newTestHandler(t)
	ctx := context.Background()

	// Empty: no activity → empty slice, no panic.
	if got := h.loadContinueWatching(ctx, 12); len(got) != 0 {
		t.Fatalf("empty: got %d items, want 0", len(got))
	}

	// In-progress TV series, last watched an hour ago.
	res, err := db.Exec("INSERT INTO libraries (name, type, root_path) VALUES ('TV', 'tv', '/tv')")
	if err != nil {
		t.Fatal(err)
	}
	tvLib, _ := res.LastInsertId()
	res, err = db.Exec(
		"INSERT INTO tv_series_files (library_id, abs_path, container, mtime_unix) VALUES (?, '/tv/show/s01e01.mkv', 'mkv', 1700000000)", tvLib)
	if err != nil {
		t.Fatal(err)
	}
	tvFile, _ := res.LastInsertId()
	if _, err := db.Exec(
		`INSERT INTO tv_series_identities (file_id, status, provider, series_id, season_number, episode_numbers_csv)
		 VALUES (?, 'matched', 'tmdb', '999', 1, '1')`, tvFile); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		"INSERT INTO tv_playback_progress (file_id, position_seconds, duration_seconds, completed, updated_at) VALUES (?, 120, 1400, 0, datetime('now', '-1 hour'))",
		tvFile); err != nil {
		t.Fatal(err)
	}
	seedTVMetadata(t, db, "999")

	// In-progress movie, last watched now (so it should sort first).
	movieFile := seedMovieFile(t, db, "Test Film", 2024, "matched", 555, h.cfg.MediaRoot)
	seedMovieMetadata(t, db, 555)
	if _, err := db.Exec(
		"INSERT INTO movie_playback_progress (file_id, position_seconds, duration_seconds, completed, updated_at) VALUES (?, 300, 6000, 0, datetime('now'))",
		movieFile); err != nil {
		t.Fatal(err)
	}

	got := h.loadContinueWatching(ctx, 12)
	if len(got) != 2 {
		t.Fatalf("got %d items, want 2 (one tv, one movie): %+v", len(got), got)
	}
	// Most-recent first: the movie (now) before the show (an hour ago).
	if got[0].Kind != "movie" || got[0].TMDBID != 555 {
		t.Fatalf("item[0] should be the movie (TMDB 555), got %+v", got[0])
	}
	if got[1].Kind != "tv" || got[1].SeriesID != "999" {
		t.Fatalf("item[1] should be the TV series 999, got %+v", got[1])
	}
	if got[0].RecencyUnix <= got[1].RecencyUnix {
		t.Fatalf("expected movie recency > tv recency, got movie=%d tv=%d", got[0].RecencyUnix, got[1].RecencyUnix)
	}

	// A completed movie must NOT appear in continue-watching.
	doneFile := seedMovieFile(t, db, "Done Film", 2023, "matched", 556, h.cfg.MediaRoot)
	seedMovieMetadata(t, db, 556)
	if _, err := db.Exec(
		"INSERT INTO movie_playback_progress (file_id, position_seconds, duration_seconds, completed, updated_at) VALUES (?, 6000, 6000, 1, datetime('now'))",
		doneFile); err != nil {
		t.Fatal(err)
	}
	got = h.loadContinueWatching(ctx, 12)
	for _, it := range got {
		if it.Kind == "movie" && it.TMDBID == 556 {
			t.Fatalf("completed movie 556 should be excluded, got %+v", got)
		}
	}
	if len(got) != 2 {
		t.Fatalf("after completed movie: got %d items, want still 2", len(got))
	}
}
