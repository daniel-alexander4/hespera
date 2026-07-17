package web

// Tests for the partial-re-watch Continue Watching rule (last-touch-wins): a
// completed episode/film whose MOST RECENT progress row carries a genuine
// mid-file position surfaces as a resume, while finished-playthrough residue
// (a first watch that earned its ✓ at 90% and quit in the credits) never does.

import (
	"context"
	"fmt"
	"testing"
)

// seedEp inserts a matched episode file with an optional progress row.
// clockOffset orders updated_at ("+N seconds" from now) so the newest touch is
// deterministic.
func seedEp(t *testing.T, h *Handler, libID int64, seriesID string, season, episode int, pos, dur float64, completed bool, clockOffset int) int64 {
	t.Helper()
	res, err := h.db.Exec(
		"INSERT INTO tv_series_files (library_id, abs_path, container, mtime_unix) VALUES (?, ?, 'mkv', 1700000000)",
		libID, fmt.Sprintf("/tv/%s/s%02de%02d.mkv", seriesID, season, episode))
	if err != nil {
		t.Fatal(err)
	}
	fileID, _ := res.LastInsertId()
	if _, err := h.db.Exec(
		`INSERT INTO tv_series_identities (file_id, status, provider, series_id, season_number, episode_numbers_csv)
		 VALUES (?, 'matched', 'tmdb', ?, ?, ?)`, fileID, seriesID, season, episode); err != nil {
		t.Fatal(err)
	}
	if pos > 0 || dur > 0 || completed {
		c := 0
		if completed {
			c = 1
		}
		if _, err := h.db.Exec(
			"INSERT INTO tv_playback_progress (file_id, position_seconds, duration_seconds, completed, updated_at) VALUES (?, ?, ?, ?, datetime('now', ?))",
			fileID, pos, dur, c, fmt.Sprintf("+%d seconds", clockOffset)); err != nil {
			t.Fatal(err)
		}
	}
	return fileID
}

func tvLib(t *testing.T, h *Handler) int64 {
	t.Helper()
	res, err := h.db.Exec("INSERT INTO libraries (name, type, root_path) VALUES ('TV', 'tv', '/tv')")
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func TestActiveResumeBoundaries(t *testing.T) {
	for _, c := range []struct {
		name            string
		pos, dur        float64
		completed, want bool
	}{
		{"completed mid-file re-watch", 400, 1000, true, true},
		{"completed just under the earn threshold", 899, 1000, true, true},
		{"completed at the 90% earn threshold = residue", 900, 1000, true, false},
		{"completed credits-quit at 92% = residue", 920, 1000, true, false},
		{"not-completed in progress", 400, 1000, false, true},
		{"not-completed at 94% still in progress", 940, 1000, false, true},
		{"not-completed past the 95% belt = missed report", 950, 1000, false, false},
		{"never started", 0, 1000, true, false},
		{"position with no duration", 400, 0, false, true},
	} {
		if got := activeResume(c.pos, c.dur, c.completed); got != c.want {
			t.Errorf("%s: activeResume(%v,%v,%v)=%v, want %v", c.name, c.pos, c.dur, c.completed, got, c.want)
		}
	}
}

func TestContinueWatchingSurfacesRewatch(t *testing.T) {
	h, db := newTestHandler(t)
	ctx := context.Background()
	lib := tvLib(t, h)

	// Series D: both episodes finished (residue positions), then E01 RE-WATCHED
	// to 40% — the newest touch. The series must surface with E01 as a resume.
	seedEp(t, h, lib, "D", 1, 2, 990, 1000, true, 1)
	e01 := seedEp(t, h, lib, "D", 1, 1, 400, 1000, true, 2) // newest: active re-watch
	seedTVMetadata(t, db, "D")

	rows, err := h.recentTVSeries(ctx, tvContinueWatchingQuery, 18)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].SeriesID != "D" || rows[0].SeasonNumber != 1 {
		t.Fatalf("re-watch series not surfaced: rows=%+v", rows)
	}
	fileID, epNum, pct, inProgress := h.continueTarget(ctx, "D", 1)
	if fileID != e01 || epNum != 1 || !inProgress || pct != 40 {
		t.Fatalf("target = (%d, E%d, %d%%, %v), want the E01 re-watch (%d, E1, 40%%, true)", fileID, epNum, pct, inProgress, e01)
	}
}

func TestFirstWatchCreditsQuitStaysPlayNext(t *testing.T) {
	h, db := newTestHandler(t)
	ctx := context.Background()
	lib := tvLib(t, h)

	// Series E: E01 earned its ✓ at 90% and quit at 92% (the newest touch);
	// E02 is unwatched. The card must say "Play next E02", not "Resume E01".
	seedEp(t, h, lib, "E", 1, 1, 920, 1000, true, 1)
	e02 := seedEp(t, h, lib, "E", 1, 2, 0, 0, false, 0)
	seedTVMetadata(t, db, "E")

	rows, err := h.recentTVSeries(ctx, tvContinueWatchingQuery, 18)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].SeasonNumber != 1 {
		t.Fatalf("series not surfaced for its unwatched episode: rows=%+v", rows)
	}
	fileID, epNum, _, inProgress := h.continueTarget(ctx, "E", 1)
	if fileID != e02 || epNum != 2 || inProgress {
		t.Fatalf("target = (%d, E%d, inProgress=%v), want fresh E02 (%d, E2, false)", fileID, epNum, inProgress, e02)
	}
}

func TestFinishedSeriesStaysHidden(t *testing.T) {
	h, db := newTestHandler(t)
	ctx := context.Background()
	lib := tvLib(t, h)

	// Series F: everything finished, only residue positions → dropped.
	seedEp(t, h, lib, "F", 1, 1, 990, 1000, true, 1)
	seedTVMetadata(t, db, "F")

	rows, err := h.recentTVSeries(ctx, tvContinueWatchingQuery, 18)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("finished series surfaced: rows=%+v", rows)
	}
}

func TestMarkWatchedZeroesPositionBothWays(t *testing.T) {
	h, _ := newTestHandler(t)
	ctx := context.Background()
	lib := tvLib(t, h)
	fileID := seedEp(t, h, lib, "G", 1, 1, 400, 1000, false, 1)

	check := func(wantPos float64, wantCompleted int, step string) {
		t.Helper()
		var pos float64
		var completed int
		if err := h.db.QueryRow("SELECT position_seconds, completed FROM tv_playback_progress WHERE file_id=?", fileID).Scan(&pos, &completed); err != nil {
			t.Fatalf("%s: %v", step, err)
		}
		if pos != wantPos || completed != wantCompleted {
			t.Fatalf("%s: pos=%v completed=%d, want pos=%v completed=%d", step, pos, completed, wantPos, wantCompleted)
		}
	}

	// Mark watched mid-file → the position is cleared too, so the episode
	// can't resurface in Continue Watching against the explicit gesture.
	if err := markWatched(ctx, h.db, "tv_playback_progress", []int64{fileID}, true); err != nil {
		t.Fatal(err)
	}
	check(0, 1, "mark watched")

	if err := markWatched(ctx, h.db, "tv_playback_progress", []int64{fileID}, false); err != nil {
		t.Fatal(err)
	}
	check(0, 0, "mark unwatched")
}

func TestMovieContinueWatchingRewatchAndResidue(t *testing.T) {
	h, db := newTestHandler(t)
	ctx := context.Background()

	seed := func(tmdbID int, title string, pos, dur float64, completed int, offset string) int64 {
		t.Helper()
		seedMovieMetadata(t, db, tmdbID)
		fid := seedMovieFile(t, db, title, 2020, "matched", tmdbID, h.cfg.MediaRoot)
		if _, err := db.Exec(
			"INSERT INTO movie_playback_progress (file_id, position_seconds, duration_seconds, completed, updated_at) VALUES (?, ?, ?, ?, datetime('now', ?))",
			fid, pos, dur, completed, offset); err != nil {
			t.Fatal(err)
		}
		return fid
	}

	rewatch := seed(601, "Rewatch Film", 400, 1000, 1, "+3 seconds") // completed, mid-file → surfaces
	seed(602, "Finished Film", 980, 1000, 1, "+2 seconds")           // completed residue → hidden
	seed(603, "Missed Report Film", 960, 1000, 0, "+1 seconds")      // not-completed past the belt → hidden

	got, err := h.loadMovieContinueWatching(ctx, 12)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].FileID != rewatch {
		t.Fatalf("movie CW = %+v, want exactly the mid-file re-watch (file %d)", got, rewatch)
	}
	if got[0].ProgressPct != 40 {
		t.Fatalf("re-watch pct = %d, want 40", got[0].ProgressPct)
	}
}
