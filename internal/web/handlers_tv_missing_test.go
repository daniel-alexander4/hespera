package web

import (
	"sort"
	"testing"

	"hespera/internal/tmdb"
)

func TestMissingSeasons(t *testing.T) {
	// TMDB knows seasons 0 (specials), 1, 2, 3. Local files for 1 and 3.
	show := tmdb.TVShow{Seasons: []tmdb.TVSeason{
		{SeasonNumber: 0, Name: "Specials"},
		{SeasonNumber: 1, Name: "Season 1"},
		{SeasonNumber: 2, Name: "Season 2"},
		{SeasonNumber: 3, Name: ""},
	}}
	present := map[int]bool{1: true, 3: true}

	got := missingSeasons(show, present)
	if len(got) != 1 {
		t.Fatalf("missing count = %d, want 1 (season 2; specials excluded, 1 & 3 present)", len(got))
	}
	if got[0].SeasonNumber != 2 || !got[0].Missing {
		t.Fatalf("got %+v, want season 2 marked missing", got[0])
	}

	t.Run("specials never missing even when absent", func(t *testing.T) {
		for _, s := range missingSeasons(show, map[int]bool{1: true, 2: true, 3: true}) {
			if s.SeasonNumber == 0 {
				t.Fatal("season 0 (specials) must never be reported missing")
			}
		}
	})

	t.Run("synthesized name for unnamed season", func(t *testing.T) {
		got := missingSeasons(tmdb.TVShow{Seasons: []tmdb.TVSeason{{SeasonNumber: 5}}}, map[int]bool{})
		if len(got) != 1 || got[0].Name != "Season 5" {
			t.Fatalf("got %+v, want a synthesized 'Season 5' name", got)
		}
	})

	t.Run("complete series yields none", func(t *testing.T) {
		if got := missingSeasons(show, map[int]bool{1: true, 2: true, 3: true}); len(got) != 0 {
			t.Fatalf("complete series should report 0 missing, got %d", len(got))
		}
	})
}

func TestMissingEpisodes(t *testing.T) {
	epCache := map[int]tmdb.TVEpisode{
		1: {EpisodeNumber: 1, Name: "Pilot"},
		2: {EpisodeNumber: 2, Name: "Second"},
		3: {EpisodeNumber: 3, Name: "Third"},
	}
	present := map[int]bool{1: true, 2: true}

	got := missingEpisodes(epCache, present)
	if len(got) != 1 {
		t.Fatalf("missing count = %d, want 1 (ep 3)", len(got))
	}
	if got[0].EpisodeNumber != 3 || got[0].Name != "Third" || !got[0].Missing {
		t.Fatalf("got %+v, want ep 3 'Third' marked missing", got[0])
	}

	t.Run("complete season yields none", func(t *testing.T) {
		if got := missingEpisodes(epCache, map[int]bool{1: true, 2: true, 3: true}); len(got) != 0 {
			t.Fatalf("complete season should report 0 missing, got %d", len(got))
		}
	})

	t.Run("non-positive episode numbers ignored", func(t *testing.T) {
		got := missingEpisodes(map[int]tmdb.TVEpisode{0: {EpisodeNumber: 0}, -1: {EpisodeNumber: -1}}, map[int]bool{})
		if len(got) != 0 {
			t.Fatalf("episode numbers <= 0 must be ignored, got %d", len(got))
		}
	})

	// Sanity: merged with present rows and sorted, gaps land in order.
	t.Run("sorts into sequence with present rows", func(t *testing.T) {
		rows := []tvEpisodeRow{{EpisodeNumber: 1}, {EpisodeNumber: 2}}
		rows = append(rows, missingEpisodes(epCache, map[int]bool{1: true, 2: true})...)
		sort.Slice(rows, func(i, j int) bool { return rows[i].EpisodeNumber < rows[j].EpisodeNumber })
		if len(rows) != 3 || rows[2].EpisodeNumber != 3 || !rows[2].Missing {
			t.Fatalf("expected ep 3 missing at the end, got %+v", rows)
		}
	})
}
