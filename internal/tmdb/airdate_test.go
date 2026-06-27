package tmdb

import "testing"

func buildIndex(t *testing.T) airDateIndex {
	t.Helper()
	idx := airDateIndex{}
	idx.add(1, []TVEpisode{
		{EpisodeNumber: 1, AirDate: "2024-01-15"},
		{EpisodeNumber: 2, AirDate: "2024-01-16"},
		{EpisodeNumber: 3, AirDate: "2024-01-17"}, // same day as a S2 episode below
	})
	idx.add(2, []TVEpisode{
		{EpisodeNumber: 5, AirDate: "2024-02-01"},
		{EpisodeNumber: 6, AirDate: "2024-02-01"}, // double episode, same season/day
		{EpisodeNumber: 7, AirDate: "2024-01-17"}, // collides with S1E3's day
	})
	// Specials share the S1 premiere date but must be excluded from the index.
	idx.add(0, []TVEpisode{{EpisodeNumber: 1, AirDate: "2024-01-15"}})
	return idx
}

func TestAirDateResolve(t *testing.T) {
	idx := buildIndex(t)

	tests := []struct {
		name       string
		date       string
		wantOK     bool
		wantSeason int
		wantCSV    string
	}{
		{"exact single", "2024-01-15", true, 1, "1"},
		{"double episode same season", "2024-02-01", true, 2, "5,6"},
		{"spans two seasons refused", "2024-01-17", false, 0, ""},
		{"no episode that day", "2024-12-25", false, 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			season, csv, ok := idx.resolve(tt.date)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if season != tt.wantSeason {
				t.Fatalf("season = %d, want %d", season, tt.wantSeason)
			}
			if csv != tt.wantCSV {
				t.Fatalf("csv = %q, want %q", csv, tt.wantCSV)
			}
		})
	}
}

func TestAirDateIndexExcludesSpecials(t *testing.T) {
	idx := buildIndex(t)
	// 2024-01-15 has a season-0 special and a S1E1; the special must not turn it
	// into a multi-season (refused) hit — it should resolve cleanly to S1E1.
	season, csv, ok := idx.resolve("2024-01-15")
	if !ok || season != 1 || csv != "1" {
		t.Fatalf("resolve = (%d, %q, %v), want (1, \"1\", true)", season, csv, ok)
	}
}
