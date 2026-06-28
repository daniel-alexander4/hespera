package tmdb

import "testing"

func TestPickBestMovieYearDisambiguation(t *testing.T) {
	// Two films share a title; the release year must pick the right one.
	results := []MovieSearchResult{
		{ID: 1, Title: "The Matrix", ReleaseDate: "1999-03-31", Popularity: 50},
		{ID: 2, Title: "The Matrix", ReleaseDate: "2021-12-22", Popularity: 90},
	}
	best, score := pickBestMovie(results, "The Matrix", 1999)
	if best == nil || best.ID != 1 {
		t.Fatalf("year 1999 should pick ID 1, got %+v (score %.3f)", best, score)
	}
	if score < movieMatchThreshold {
		t.Errorf("exact title+year should clear threshold, got %.3f", score)
	}

	// The same query with the later year flips the winner despite ID 2's higher
	// popularity proving the year signal dominates a popularity tilt.
	best, _ = pickBestMovie(results, "The Matrix", 2021)
	if best == nil || best.ID != 2 {
		t.Fatalf("year 2021 should pick ID 2, got %+v", best)
	}
}

func TestPickBestMovieUnknownYearFallsBackToPopularity(t *testing.T) {
	results := []MovieSearchResult{
		{ID: 1, Title: "Dune", ReleaseDate: "1984-12-14", Popularity: 20},
		{ID: 2, Title: "Dune", ReleaseDate: "2021-10-22", Popularity: 80},
	}
	best, _ := pickBestMovie(results, "Dune", 0)
	if best == nil || best.ID != 2 {
		t.Fatalf("no year → highest popularity (ID 2), got %+v", best)
	}
}

func TestPickBestMovieRejectsWeakTitle(t *testing.T) {
	results := []MovieSearchResult{
		{ID: 1, Title: "A Totally Unrelated Film", ReleaseDate: "1999-01-01", Popularity: 5},
	}
	_, score := pickBestMovie(results, "The Matrix", 1999)
	if score >= movieMatchThreshold {
		t.Errorf("an unrelated title should not clear the threshold even with an exact year, got %.3f", score)
	}
}

func TestPickBestMovieEmpty(t *testing.T) {
	best, score := pickBestMovie(nil, "x", 2000)
	if best != nil || score != 0 {
		t.Errorf("empty results → (nil, 0), got (%+v, %.3f)", best, score)
	}
}
