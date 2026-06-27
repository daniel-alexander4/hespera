package tvscan

import (
	"testing"
)

func TestIdentifyFile(t *testing.T) {
	tests := []struct {
		path      string
		wantTitle string
		wantS     int
		wantE     []int
		wantConf  float64
		wantNil   bool
	}{
		{
			path:      "/tv/Breaking Bad/Season 1/Breaking Bad S01E01 Pilot.mkv",
			wantTitle: "Breaking Bad",
			wantS:     1,
			wantE:     []int{1},
			wantConf:  0.72,
		},
		{
			path:      "/tv/The Office/The.Office.US.S02E03.720p.BluRay.mkv",
			wantTitle: "The Office US",
			wantS:     2,
			wantE:     []int{3},
			wantConf:  0.72,
		},
		{
			path:      "/tv/Show/Season 3/1x05 - Title.mp4",
			wantTitle: "",
			wantS:     1,
			wantE:     []int{5},
			wantConf:  0.55,
		},
		{
			path:      "/tv/Show/Season 1/S01E01E02.mkv",
			wantTitle: "",
			wantS:     1,
			wantE:     []int{1, 2},
			wantConf:  0.55,
		},
		{
			path:      "/tv/Lost/Season 2/some random file.mkv",
			wantTitle: "Lost",
			wantS:     2,
			wantE:     nil,
			wantConf:  0.30,
		},
		{
			// Short "sN" season dir is recognized; show title comes from the
			// show folder, not the inconsistent filename (year stripped by
			// folder authority, not text munging).
			path:      "/tv/Murderbot/s1/Murderbot (2025) - S01E01 - FreeCommerce.mkv",
			wantTitle: "Murderbot",
			wantS:     1,
			wantE:     []int{1},
			wantConf:  0.72,
		},
		{
			// Sibling file with a different release style resolves to the SAME
			// title via the show folder — the two group together.
			path:      "/tv/Murderbot/s1/Murderbot.S01E01.1080p.WEB.h264-ETHEL.mkv",
			wantTitle: "Murderbot",
			wantS:     1,
			wantE:     []int{1},
			wantConf:  0.72,
		},
		{
			// x_format with no title in the filename recovers the show name from
			// the folder above the season dir (two levels up).
			path:      "/tv/Monty Pythons Flying Circus/s1/1x01 Whither Canada.mkv",
			wantTitle: "Monty Pythons Flying Circus",
			wantS:     1,
			wantE:     []int{1},
			wantConf:  0.55,
		},
		{
			path:    "/tv/random.txt",
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			id := IdentifyFile(tt.path)
			if tt.wantNil {
				if id != nil {
					t.Fatalf("expected nil, got %+v", id)
				}
				return
			}
			if id == nil {
				t.Fatalf("expected non-nil identity")
			}
			if tt.wantTitle != "" && id.ShowTitle != tt.wantTitle {
				t.Fatalf("ShowTitle = %q, want %q", id.ShowTitle, tt.wantTitle)
			}
			if id.SeasonNumber != tt.wantS {
				t.Fatalf("SeasonNumber = %d, want %d", id.SeasonNumber, tt.wantS)
			}
			if tt.wantE != nil {
				if len(id.EpisodeNumbers) != len(tt.wantE) {
					t.Fatalf("EpisodeNumbers = %v, want %v", id.EpisodeNumbers, tt.wantE)
				}
				for i := range tt.wantE {
					if id.EpisodeNumbers[i] != tt.wantE[i] {
						t.Fatalf("EpisodeNumbers[%d] = %d, want %d", i, id.EpisodeNumbers[i], tt.wantE[i])
					}
				}
			}
			if id.Confidence != tt.wantConf {
				t.Fatalf("Confidence = %v, want %v", id.Confidence, tt.wantConf)
			}
		})
	}
}

func TestParseSeasonDir(t *testing.T) {
	tests := []struct {
		dir  string
		want int
		ok   bool
	}{
		{"Season 1", 1, true},
		{"season 3", 3, true},
		{"Season03", 3, true},
		{"season 12", 12, true},
		{"s1", 1, true},
		{"S01", 1, true},
		{"s 2", 2, true},
		{"Specials", 0, false},
		{"S01E01", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.dir, func(t *testing.T) {
			got, ok := ParseSeasonDir(tt.dir)
			if ok != tt.ok || got != tt.want {
				t.Fatalf("ParseSeasonDir(%q) = (%d, %v), want (%d, %v)", tt.dir, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestCleanTitle(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"Breaking.Bad", "Breaking Bad"},
		{"The_Office_US", "The Office US"},
		{"Show.Name.720p.BluRay", "Show Name"},
		{"Show-Name-x265", "Show Name"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got := cleanTitle(tt.in)
			if got != tt.want {
				t.Fatalf("cleanTitle(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestIdentifyXFormat(t *testing.T) {
	id := IdentifyFile("/tv/Friends/Friends 3x14 The One with Phoebe's Ex.avi")
	if id == nil {
		t.Fatal("expected non-nil")
	}
	if id.Method != "x_format" {
		t.Fatalf("method = %q, want x_format", id.Method)
	}
	if id.SeasonNumber != 3 {
		t.Fatalf("season = %d, want 3", id.SeasonNumber)
	}
	if len(id.EpisodeNumbers) != 1 || id.EpisodeNumbers[0] != 14 {
		t.Fatalf("episodes = %v, want [14]", id.EpisodeNumbers)
	}
	if id.ShowTitle != "Friends" {
		t.Fatalf("title = %q, want Friends (from filename, no season dir)", id.ShowTitle)
	}
}

func TestIdentifyAirDate(t *testing.T) {
	// Year-first dates resolve to method=airdate with the show title and an
	// unknown (-1) season; the matcher fills season/episode later.
	resolves := []struct {
		path      string
		wantTitle string
		wantDate  string
	}{
		{"/tv/The Tonight Show/The.Tonight.Show.2024-01-15.mkv", "The Tonight Show", "2024-01-15"},
		{"/tv/The Tonight Show/The.Tonight.Show.2024.01.15.mkv", "The Tonight Show", "2024-01-15"},
		{"/tv/Colbert/Colbert.2024-01-15.Guest.Name.1080p.mkv", "Colbert", "2024-01-15"},
	}
	for _, tt := range resolves {
		id := IdentifyFile(tt.path)
		if id == nil || id.Method != "airdate" {
			t.Fatalf("%s: want method airdate, got %+v", tt.path, id)
		}
		if id.AirDate != tt.wantDate {
			t.Fatalf("%s: AirDate = %q, want %q", tt.path, id.AirDate, tt.wantDate)
		}
		if id.ShowTitle != tt.wantTitle {
			t.Fatalf("%s: ShowTitle = %q, want %q", tt.path, id.ShowTitle, tt.wantTitle)
		}
		if id.SeasonNumber != -1 {
			t.Fatalf("%s: SeasonNumber = %d, want -1", tt.path, id.SeasonNumber)
		}
	}

	// These must NOT be read as air dates: a bare year, year-as-season, a year
	// in the title (resolved by its SxE marker), and impossible dates.
	for _, path := range []string{
		"/tv/Show/Show.2024.mkv",
		"/tv/The Daily Show/The.Daily.Show.S2024E12.mkv",
		"/tv/Stranger Things/Season 4/Stranger.Things.2016.S04E01.mkv",
		"/tv/Show/Show.2020.13.45.mkv",
		"/tv/Show/Show.2020.40.50.mkv",
	} {
		id := IdentifyFile(path)
		if id != nil && id.Method == "airdate" {
			t.Fatalf("%s: should not parse as airdate, got %+v", path, id)
		}
	}
}

// Without a season directory and without a title in the filename, the
// identifier must NOT manufacture a show title from an arbitrary container
// folder — it leaves the title empty, consistent across sxe and x_format.
func TestIdentifyNoJunkTitle(t *testing.T) {
	for _, path := range []string{
		"/downloads/1x05.mkv",
		"/downloads/S01E05.mkv",
	} {
		id := IdentifyFile(path)
		if id == nil {
			t.Fatalf("%s: expected non-nil identity", path)
		}
		if id.ShowTitle != "" {
			t.Fatalf("%s: ShowTitle = %q, want empty", path, id.ShowTitle)
		}
	}
}
