package tvscan

import (
	"fmt"
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
	fmt.Printf("title=%q\n", id.ShowTitle)
}
