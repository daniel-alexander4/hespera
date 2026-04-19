package tmdb

import (
	"testing"
)

func TestPickBestResult(t *testing.T) {
	t.Run("empty_nil_results", func(t *testing.T) {
		// nil input
		res, score := pickBestResult(nil, "anything")
		if res != nil {
			t.Fatalf("expected nil result for nil input, got %+v", res)
		}
		if score != 0 {
			t.Fatalf("expected score 0 for nil input, got %v", score)
		}

		// empty slice
		res, score = pickBestResult([]TVSearchResult{}, "anything")
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
		res, score := pickBestResult(results, "Breaking Bad")
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

	t.Run("picks_highest_scorer", func(t *testing.T) {
		results := []TVSearchResult{
			{ID: 1396, Name: "Breaking Bad", Popularity: 234.5},
			{ID: 999, Name: "Breaking Bad: Criminal Elements", Popularity: 12.3},
		}
		res, _ := pickBestResult(results, "Breaking Bad")
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
		_, score := pickBestResult(results, "Breaking Bad")
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
		_, score := pickBestResult(results, "Breaking Bad")
		// Exact match with zero popularity: score = 1.0 + 0.0 = 1.0
		if score < 0.80 {
			t.Fatalf("score = %v, exact match should be >= 0.80", score)
		}
	})

	t.Run("below_threshold_dissimilar", func(t *testing.T) {
		results := []TVSearchResult{
			{ID: 1, Name: "Breaking Bad", Popularity: 100},
		}
		_, score := pickBestResult(results, "Totally Different Show")
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
		_, score := pickBestResult(results, "Breakng Bad X")
		if score >= 0.80 {
			t.Fatalf("score = %v, want < 0.80 for near-boundary case", score)
		}
		// Verify it's meaningfully close to the boundary (above 0.50).
		if score < 0.50 {
			t.Fatalf("score = %v, expected near-boundary (> 0.50)", score)
		}
	})
}
