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
			// Real library file: flat SxE specials (season 0).
			path:      "/tv/Burnistoun/Burnistoun S00E00 Pilot.mkv",
			wantTitle: "Burnistoun",
			wantS:     0,
			wantE:     []int{0},
			wantConf:  0.72,
		},
		{
			// Real library file: trailing [release.tracker] tag after the episode
			// must not be eaten into the episode block.
			path:      "/tv/Murderbot/s1/Murderbot.S01E01.1080p.WEB.h264-ETHEL[EZTVx.to].mkv",
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
			// SxE with a separator between S and E.
			path:      "/tv/Show/Season 1/Show.S01.E07.mkv",
			wantTitle: "Show",
			wantS:     1,
			wantE:     []int{7},
			wantConf:  0.72,
		},
		{
			path:      "/tv/Show/Season 1/Show S01 E08.mkv",
			wantTitle: "Show",
			wantS:     1,
			wantE:     []int{8},
			wantConf:  0.72,
		},
		{
			// SxE range — both episodes recovered, not just the first.
			path:      "/tv/Show/Season 1/Show.S01E01-E02.mkv",
			wantTitle: "Show",
			wantS:     1,
			wantE:     []int{1, 2},
			wantConf:  0.72,
		},
		{
			path:      "/tv/Show/Season 1/Show.S01E05-07.mkv",
			wantTitle: "Show",
			wantS:     1,
			wantE:     []int{5, 6, 7},
			wantConf:  0.72,
		},
		{
			// NxM range (no filename title → dir-inferred, 0.55).
			path:     "/tv/Show/Season 1/1x05-06 Title.mkv",
			wantS:    1,
			wantE:    []int{5, 6},
			wantConf: 0.55,
		},
		{
			// Live bug guard: a resolution string must NOT parse as season/episode.
			// With an explicit SxE present, SxE wins and 1280x720 is ignored.
			path:      "/tv/Show/Season 1/Show.S01E01.1280x720.x264.mkv",
			wantTitle: "Show",
			wantS:     1,
			wantE:     []int{1},
			wantConf:  0.72,
		},
		{
			// A bare resolution / NxN-with-1-digit-episode yields no identity.
			path:    "/downloads/1280x720.mkv",
			wantNil: true,
		},
		{
			path:    "/downloads/720x480.mkv",
			wantNil: true,
		},
		{
			path:    "/downloads/4x4.mkv",
			wantNil: true,
		},
		{
			// Season-dir-relative: "Episode N" resolves against the season folder.
			path:     "/tv/Show/Season 2/Show - Episode 5.mkv",
			wantS:    2,
			wantE:    []int{5},
			wantConf: 0.60,
		},
		{
			// Bare-number file under a season dir is the episode.
			path:     "/tv/Show/Season 3/07.mkv",
			wantS:    3,
			wantE:    []int{7},
			wantConf: 0.60,
		},
		{
			// E-only marker under a season dir.
			path:     "/tv/Show/Season 1/Show E09.mkv",
			wantS:    1,
			wantE:    []int{9},
			wantConf: 0.60,
		},
		{
			// Group-tagged release: the [group] prefix is stripped from the title.
			path:      "/tv/[RlsGroup] Some Show - S01E04.mkv",
			wantTitle: "Some Show",
			wantS:     1,
			wantE:     []int{4},
			wantConf:  0.72,
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
		{"Specials", 0, true},
		{"specials", 0, true},
		{"Series 2", 2, true},
		{"Saison 1", 1, true},
		{"Staffel 3", 3, true},
		{"Temporada 4", 4, true},
		{"Season.1", 1, true},
		{"Season_1", 1, true},
		{"S01E01", 0, false},
		{"Extras", 0, false},
		{"5", 0, false},
		{"Downloads", 0, false},
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
		{"[RlsGroup] Some Show", "Some Show"},
		{"{abc123} Show Name", "Show Name"},
		{"www.site.com - Show Name", "Show Name"},
		{"[A][B] Show Name", "Show Name"},
		// Season/episode markers embedded in a folder-derived title are stripped.
		{"The.Great.British.Bake.Off S13 An Extra Slice S09", "The Great British Bake Off An Extra Slice"},
		{"Show.Name.S01", "Show Name"},
		{"Show.Name.S01E02", "Show Name"},
		{"Show.Name.S01E01E02", "Show Name"},
		// FP guards: real title tokens that must NOT be stripped.
		{"S Club 7", "S Club 7"},          // "S" alone (no digit) survives
		{"Series 7", "Series 7"},          // not an s+digits token
		{"District 13", "District 13"},    // bare number survives
		{"Se7en", "Se7en"},                // digit inside a word survives
		{"3x3 Eyes", "3x3 Eyes"},          // NxM is not touched by this strip
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

func TestIsJunkFile(t *testing.T) {
	junk := []string{
		"Show.S01E01.720p.WEB-DL.sample",
		"Show S01E01 sample",
		"movie-sample",
	}
	for _, s := range junk {
		if !IsJunkFile(s) {
			t.Errorf("IsJunkFile(%q) = false, want true", s)
		}
	}
	keep := []string{
		"Trailer Park Boys S01E01", // leading "Trailer" is a title word
		"Show S01E01",
		"Sample Text Show S01E01", // "Sample" leading is a title word, not a tag
	}
	for _, s := range keep {
		if IsJunkFile(s) {
			t.Errorf("IsJunkFile(%q) = true, want false", s)
		}
	}
}

func TestIsJunkDirName(t *testing.T) {
	for _, d := range []string{"Sample", "samples"} {
		if !IsJunkDirName(d) {
			t.Errorf("IsJunkDirName(%q) = false, want true", d)
		}
	}
	// Extras-type dirs are NOT junk anymore — they classify playable extras.
	for _, d := range []string{"Breaking Bad", "Season 1", "Extra Life", "Trailer Park Boys", "Extras", "Trailers", "Featurettes"} {
		if IsJunkDirName(d) {
			t.Errorf("IsJunkDirName(%q) = true, want false", d)
		}
	}
}

func TestExtrasDirCategory(t *testing.T) {
	cases := map[string]string{
		"Extras":            "Extra",
		"featurettes":       "Featurette",
		"Trailers":          "Trailer",
		"Behind The Scenes": "Behind the Scenes",
		"Deleted Scenes":    "Deleted Scene",
		"Interviews":        "Interview",
		"Shorts":            "Short",
	}
	for name, want := range cases {
		got, ok := ExtrasDirCategory(name)
		if !ok || got != want {
			t.Errorf("ExtrasDirCategory(%q) = %q,%v, want %q,true", name, got, ok, want)
		}
	}
	for _, name := range []string{"Sample", "Season 1", "Scenes", "Other", "Extra Life"} {
		if _, ok := ExtrasDirCategory(name); ok {
			t.Errorf("ExtrasDirCategory(%q) = true, want false", name)
		}
	}
}

func TestClassifyExtra(t *testing.T) {
	root := "/media/tv"
	cases := []struct {
		path        string
		rootIsTitle bool
		wantCat     string
		wantOK      bool
	}{
		// Nested under a show folder → extra.
		{"/media/tv/Show/Extras/making of.mkv", false, "Extra", true},
		{"/media/tv/Show/Trailers/teaser.mkv", false, "Trailer", true},
		{"/media/tv/Show/Season 1/Extras/gag reel.mkv", false, "Extra", true},
		{"/media/tv/Show/Behind The Scenes/day one.mkv", false, "Behind the Scenes", true},
		// Top-level under the library root → a real title named like the dir.
		{"/media/tv/Extras/S01E01.mkv", false, "", false},
		{"/media/tv/Trailers/pilot.mkv", false, "", false},
		// Per-series scoped walk: the root IS the show folder, first level counts.
		{"/media/tv/Extras/clip.mkv", true, "Extra", true},
		// Regular episode paths.
		{"/media/tv/Show/Season 1/S01E01.mkv", false, "", false},
		{"/media/tv/Show/episode.mkv", false, "", false},
	}
	for _, c := range cases {
		cat, ok := ClassifyExtra(c.path, root, c.rootIsTitle)
		if ok != c.wantOK || cat != c.wantCat {
			t.Errorf("ClassifyExtra(%q, rootIsTitle=%v) = %q,%v, want %q,%v", c.path, c.rootIsTitle, cat, ok, c.wantCat, c.wantOK)
		}
	}
}

func TestExtraTitle(t *testing.T) {
	cases := map[string]string{
		"/x/Show/Extras/Making.of.the.Show.mkv":       "Making of the Show",
		"/x/Show/Extras/[group] deleted_scene_01.mkv": "deleted scene 01",
		"/x/Show/Extras/interview.mkv":                "interview",
	}
	for path, want := range cases {
		if got := ExtraTitle(path); got != want {
			t.Errorf("ExtraTitle(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestExtrasOwnerDir(t *testing.T) {
	root := "/media/tv"
	cases := []struct {
		path      string
		wantOwner string
		wantOK    bool
	}{
		{"/media/tv/Show/Extras/x.mkv", "/media/tv/Show", true},
		{"/media/tv/Show/Season 1/Extras/x.mkv", "/media/tv/Show", true}, // season dir rolls up to the show
		{"/media/tv/Show/Trailers/x.mkv", "/media/tv/Show", true},
		{"/media/tv/Extras/x.mkv", "", false}, // top-level: a real title, no owner
		{"/media/tv/Show/Season 1/S01E01.mkv", "", false},
	}
	for _, c := range cases {
		owner, ok := ExtrasOwnerDir(c.path, root)
		if ok != c.wantOK || owner != c.wantOwner {
			t.Errorf("ExtrasOwnerDir(%q) = %q,%v, want %q,%v", c.path, owner, ok, c.wantOwner, c.wantOK)
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

func TestIdentifyFolderYear(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		wantTitle string
		wantYear  int
		wantSeas  int
	}{
		{"parenthesized folder year, flat layout", "/media/tv/Doctor Who (2023)/Doctor.Who.2023.S02E01.720p.x264.mkv", "Doctor Who", 2023, 2},
		{"folder year + season dir", "/media/tv/Show Name (2019)/Season 1/Show.Name.S01E01.mkv", "Show Name", 2019, 1},
		{"trailing bare folder year", "/media/tv/Doctor Who 2005/Doctor.Who.S01E01.mkv", "Doctor Who", 2005, 1},
		{"year-titled show keeps its title (no year)", "/media/tv/1883/1883.S01E01.mkv", "1883", 0, 1},
		{"no folder year → unchanged (filename title)", "/media/tv/Breaking Bad/Breaking.Bad.S01E01.mkv", "Breaking Bad", 0, 1},
		{"no folder year, season dir → folder title, no year", "/media/tv/Doctor Who/Season 2/Doctor.Who.S02E03.mkv", "Doctor Who", 0, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IdentifyFile(tt.path)
			if got == nil {
				t.Fatalf("IdentifyFile(%q) = nil", tt.path)
			}
			if got.ShowTitle != tt.wantTitle {
				t.Errorf("title = %q, want %q", got.ShowTitle, tt.wantTitle)
			}
			if got.Year != tt.wantYear {
				t.Errorf("year = %d, want %d", got.Year, tt.wantYear)
			}
			if got.SeasonNumber != tt.wantSeas {
				t.Errorf("season = %d, want %d", got.SeasonNumber, tt.wantSeas)
			}
		})
	}
}
