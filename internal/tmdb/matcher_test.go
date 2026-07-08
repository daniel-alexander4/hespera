package tmdb

import (
	"testing"
)

func TestPickBestResult(t *testing.T) {
	t.Run("empty_nil_results", func(t *testing.T) {
		// nil input
		res, score := pickBestResult(nil, "anything", 0)
		if res != nil {
			t.Fatalf("expected nil result for nil input, got %+v", res)
		}
		if score != 0 {
			t.Fatalf("expected score 0 for nil input, got %v", score)
		}

		// empty slice
		res, score = pickBestResult([]TVSearchResult{}, "anything", 0)
		if res != nil {
			t.Fatalf("expected nil result for empty input, got %+v", res)
		}
		if score != 0 {
			t.Fatalf("expected score 0 for empty input, got %v", score)
		}
	})

	t.Run("single_exact_match", func(t *testing.T) {
		results := []TVSearchResult{
			{ID: 1396, Name: "Breaking Bad", Popularity: 100},
		}
		res, score := pickBestResult(results, "Breaking Bad", 0)
		if res == nil {
			t.Fatalf("expected non-nil result")
		}
		if res.ID != 1396 {
			t.Fatalf("ID = %d, want 1396", res.ID)
		}
		// Exact match similarity = 1.0 + pop bonus (100/10000 = 0.01) = 1.01
		if score < 0.80 {
			t.Fatalf("score = %v, want >= 0.80", score)
		}
	})

	t.Run("matches_original_name", func(t *testing.T) {
		// Folder uses the native title; TMDB's display name is the English one.
		results := []TVSearchResult{
			{ID: 71446, Name: "Money Heist", OriginalName: "La Casa de Papel", Popularity: 100},
		}
		res, score := pickBestResult(results, "La Casa de Papel", 0)
		if res == nil || res.ID != 71446 {
			t.Fatalf("expected id 71446, got %+v", res)
		}
		if score < 0.80 {
			t.Fatalf("score = %v via original_name, want >= 0.80", score)
		}
	})

	t.Run("picks_highest_scorer", func(t *testing.T) {
		results := []TVSearchResult{
			{ID: 1396, Name: "Breaking Bad", Popularity: 234.5},
			{ID: 999, Name: "Breaking Bad: Criminal Elements", Popularity: 12.3},
		}
		res, _ := pickBestResult(results, "Breaking Bad", 0)
		if res == nil {
			t.Fatalf("expected non-nil result")
		}
		if res.ID != 1396 {
			t.Fatalf("ID = %d, want 1396 (exact match should win)", res.ID)
		}
	})

	t.Run("popularity_bonus_capped", func(t *testing.T) {
		// With Popularity=50000, uncapped bonus would be 5.0.
		// Capped at 0.1, so max possible score is similarity + 0.1.
		results := []TVSearchResult{
			{ID: 1, Name: "Breaking Bad", Popularity: 50000},
		}
		_, score := pickBestResult(results, "Breaking Bad", 0)
		// Exact match similarity = 1.0, capped pop bonus = 0.1, so score should be 1.1.
		if score > 1.1+0.001 {
			t.Fatalf("score = %v, exceeds similarity+0.1 cap", score)
		}
		// Also verify it IS capped at 0.1 (not the uncapped 5.0).
		// similarity(exact) = 1.0, so score should be exactly 1.1.
		if score < 1.09 || score > 1.11 {
			t.Fatalf("score = %v, want ~1.1 (1.0 similarity + 0.1 capped bonus)", score)
		}
	})

	t.Run("above_threshold_exact", func(t *testing.T) {
		results := []TVSearchResult{
			{ID: 1396, Name: "Breaking Bad", Popularity: 0},
		}
		_, score := pickBestResult(results, "Breaking Bad", 0)
		// Exact match with zero popularity: score = 1.0 + 0.0 = 1.0
		if score < 0.80 {
			t.Fatalf("score = %v, exact match should be >= 0.80", score)
		}
	})

	t.Run("below_threshold_dissimilar", func(t *testing.T) {
		results := []TVSearchResult{
			{ID: 1, Name: "Breaking Bad", Popularity: 100},
		}
		_, score := pickBestResult(results, "Totally Different Show", 0)
		// NormalizedSimilarity("Totally Different Show", "Breaking Bad") ~ 0.1818
		if score >= 0.80 {
			t.Fatalf("score = %v, dissimilar query should be < 0.80", score)
		}
	})

	t.Run("near_boundary_below", func(t *testing.T) {
		// "Breakng Bad X" vs "Breaking Bad" gives NormalizedSimilarity ~0.7692
		// With Popularity=0, pop bonus = 0, score = ~0.7692 < 0.80.
		results := []TVSearchResult{
			{ID: 1, Name: "Breaking Bad", Popularity: 0},
		}
		_, score := pickBestResult(results, "Breakng Bad X", 0)
		if score >= 0.80 {
			t.Fatalf("score = %v, want < 0.80 for near-boundary case", score)
		}
		// Verify it's meaningfully close to the boundary (above 0.50).
		if score < 0.50 {
			t.Fatalf("score = %v, expected near-boundary (> 0.50)", score)
		}
	})
}

func TestPickBestResultYearDisambiguation(t *testing.T) {
	// Two same-named eras; the more popular one is the wrong year.
	results := []TVSearchResult{
		{ID: 57243, Name: "Doctor Who", FirstAirDate: "2005-03-26", Popularity: 200},
		{ID: 121340, Name: "Doctor Who", FirstAirDate: "2023-11-25", Popularity: 50},
	}
	best, score := pickBestResult(results, "Doctor Who", 2023)
	if best == nil || best.ID != 121340 {
		t.Fatalf("year 2023 should pick the 2023 series, got %+v (score %.3f)", best, score)
	}
	// No year hint → falls back to popularity (the 2005 series).
	best, _ = pickBestResult(results, "Doctor Who", 0)
	if best == nil || best.ID != 57243 {
		t.Fatalf("no year → highest popularity (2005), got %+v", best)
	}
}
