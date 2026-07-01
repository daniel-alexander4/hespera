package web

import (
	"context"
	"fmt"
	"testing"
)

// seedIntroEpisode inserts a matched episode for a series under one library.
func seedIntroEpisode(t *testing.T, h *Handler, libraryID int64, seriesID string, season, ep int) {
	t.Helper()
	res, err := h.db.Exec(`INSERT INTO tv_series_files (library_id, abs_path) VALUES (?, ?)`,
		libraryID, fmt.Sprintf("/media/%s/s%02de%02d.mkv", seriesID, season, ep))
	if err != nil {
		t.Fatalf("seed file: %v", err)
	}
	fid, _ := res.LastInsertId()
	if _, err := h.db.Exec(
		`INSERT INTO tv_series_identities (file_id, status, provider, series_id, season_number, episode_numbers_csv)
		 VALUES (?, 'matched', 'tmdb', ?, ?, ?)`, fid, seriesID, season, fmt.Sprint(ep)); err != nil {
		t.Fatalf("seed identity: %v", err)
	}
}

// gatherIntroSeasons must group by season, scope to one season when asked, and
// count only seasons with >=2 episodes (cross-match needs a pair) into total.
func TestGatherIntroSeasons(t *testing.T) {
	h, _ := newTestHandler(t)
	ctx := context.Background()
	if _, err := h.db.Exec(`INSERT INTO libraries (id, name, type, root_path) VALUES (7, 'TV', 'tv', '/media')`); err != nil {
		t.Fatal(err)
	}
	// Series 999: season 1 has 2 episodes (detectable), season 2 has 1 (not).
	seedIntroEpisode(t, h, 7, "999", 1, 1)
	seedIntroEpisode(t, h, 7, "999", 1, 2)
	seedIntroEpisode(t, h, 7, "999", 2, 1)

	bySeason, lib, total, err := h.gatherIntroSeasons(ctx, "999", 0) // whole series
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	if lib != 7 {
		t.Fatalf("libraryID = %d, want 7", lib)
	}
	if len(bySeason[1]) != 2 || len(bySeason[2]) != 1 {
		t.Fatalf("bySeason = %v, want s1:2 s2:1", bySeason)
	}
	if total != 2 { // only season 1 (>=2 episodes) counts
		t.Fatalf("total = %d, want 2 (single-episode season 2 excluded)", total)
	}

	// Season filter scopes to one season.
	if _, _, total1, _ := h.gatherIntroSeasons(ctx, "999", 1); total1 != 2 {
		t.Fatalf("season 1 total = %d, want 2", total1)
	}
	if _, _, total2, _ := h.gatherIntroSeasons(ctx, "999", 2); total2 != 0 {
		t.Fatalf("season 2 total = %d, want 0 (single episode → nothing to detect)", total2)
	}
}

// The per-season marker gates the lazy auto-detect so it never re-runs.
func TestIntroMarkerGate(t *testing.T) {
	h, _ := newTestHandler(t)
	ctx := context.Background()

	key := introMarkerKey("999", 2)
	if key != "show:999:introskip:s2" {
		t.Fatalf("marker key = %q, want show:999:introskip:s2", key)
	}
	if h.metaMarkerExists(ctx, key) {
		t.Fatal("marker should not exist before detection")
	}
	if _, err := h.db.Exec(
		`INSERT INTO tv_series_metadata_cache (entity_key, lang, payload_json, fetched_at) VALUES (?, 'en', '{}', datetime('now'))`,
		key); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	if !h.metaMarkerExists(ctx, key) {
		t.Fatal("marker should exist after detection writes it → lazy trigger is gated off")
	}
}
