package web

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// Up Next: at a season's last episode the next target rolls into the next
// local season's first episode, and the home Continue-Watching card resolves
// the target season's first unwatched file for its one-click play link.

func TestUpNextCrossSeasonAndFirstUnwatched(t *testing.T) {
	h, db := newTestHandler(t)
	router := h.Router()
	ctx := context.Background()

	res, err := db.Exec("INSERT INTO libraries (name, type, root_path) VALUES ('TV', 'tv', ?)", h.cfg.MediaRoot)
	if err != nil {
		t.Fatal(err)
	}
	libID, _ := res.LastInsertId()
	seed := func(season, ep int) int64 {
		fres, err := db.Exec(
			"INSERT INTO tv_series_files (library_id, abs_path, container) VALUES (?, ?, 'mkv')",
			libID, filepath.Join(h.cfg.MediaRoot, fmt.Sprintf("u.s%02de%02d.mkv", season, ep)))
		if err != nil {
			t.Fatal(err)
		}
		fid, _ := fres.LastInsertId()
		if _, err := db.Exec(
			`INSERT INTO tv_series_identities (file_id, provider, series_id, status, guessed_title, season_number, episode_numbers_csv)
			 VALUES (?, 'tmdb', '700', 'matched', 'Roll Show', ?, ?)`, fid, season, fmt.Sprint(ep)); err != nil {
			t.Fatal(err)
		}
		return fid
	}
	s1e1, s1e2, s2e1 := seed(1, 1), seed(1, 2), seed(2, 1)

	// Cross-season roll: after S1's last episode, next = S2E1; S2 is the end.
	if got := h.nextSeasonFirstEpisode(ctx, "700", 1); got != s2e1 {
		t.Fatalf("next season first = %d, want %d", got, s2e1)
	}
	if got := h.nextSeasonFirstEpisode(ctx, "700", 2); got != 0 {
		t.Fatalf("last season should have no next, got %d", got)
	}

	// The player page bakes the rolled id into data-next-file at S1's last ep.
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/tv/player?file=%d", s1e2), nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("player page: %d — %s", rec.Code, rec.Body.String())
	}
	if want := fmt.Sprintf(`data-next-file="%d"`, s2e1); !strings.Contains(rec.Body.String(), want) {
		t.Fatalf("player page should carry the cross-season next (%s)", want)
	}

	// firstUnwatchedInSeason: skips completed episodes, 0 when all watched.
	if got := h.firstUnwatchedInSeason(ctx, "700", 1); got != s1e1 {
		t.Fatalf("first unwatched = %d, want %d", got, s1e1)
	}
	if _, err := db.Exec("INSERT INTO tv_playback_progress (file_id, completed, updated_at) VALUES (?, 1, datetime('now'))", s1e1); err != nil {
		t.Fatal(err)
	}
	if got := h.firstUnwatchedInSeason(ctx, "700", 1); got != s1e2 {
		t.Fatalf("first unwatched after e1 done = %d, want %d", got, s1e2)
	}
	if _, err := db.Exec("INSERT INTO tv_playback_progress (file_id, completed, updated_at) VALUES (?, 1, datetime('now'))", s1e2); err != nil {
		t.Fatal(err)
	}
	if got := h.firstUnwatchedInSeason(ctx, "700", 1); got != 0 {
		t.Fatalf("fully-watched season should yield 0, got %d", got)
	}

	// The home Continue-Watching card carries the play target.
	seedTVMetadata(t, db, "700")
	items := h.loadContinueWatching(ctx, 12)
	if len(items) != 1 || items[0].Kind != "tv" {
		t.Fatalf("expected one tv continue item, got %+v", items)
	}
	// S1 fully watched → CW rolled the target season to S2; its first unwatched is S2E1.
	if items[0].NextFileID != s2e1 {
		t.Fatalf("CW play target = %d, want %d", items[0].NextFileID, s2e1)
	}
}
