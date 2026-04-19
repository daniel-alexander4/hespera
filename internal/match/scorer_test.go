package match

import "testing"

func TestScoreCandidate(t *testing.T) {
	tests := []struct {
		name      string
		candidate Candidate
		title     string
		artist    string
		year      int
		minScore  float64
		maxScore  float64
	}{
		{
			name: "perfect match",
			candidate: Candidate{
				Title:       "Abbey Road",
				ArtistName:  "The Beatles",
				PrimaryType: "Album",
				MBScore:     100,
				Year:        1969,
			},
			title: "Abbey Road", artist: "The Beatles", year: 1969,
			minScore: 90, maxScore: 96,
		},
		{
			name: "matched threshold boundary",
			candidate: Candidate{
				Title:       "Abbey Road",
				ArtistName:  "The Beatles",
				PrimaryType: "Album",
				MBScore:     80,
				Year:        1969,
			},
			title: "Abbey Road", artist: "The Beatles", year: 1969,
			minScore: 70, maxScore: 96,
		},
		{
			name: "single type penalty",
			candidate: Candidate{
				Title:       "Hey Jude",
				ArtistName:  "The Beatles",
				PrimaryType: "Single",
				MBScore:     100,
				Year:        1968,
			},
			title: "Hey Jude", artist: "The Beatles", year: 1968,
			minScore: 80, maxScore: 90,
		},
		{
			name: "different artist",
			candidate: Candidate{
				Title:       "Abbey Road",
				ArtistName:  "Radiohead",
				PrimaryType: "Album",
				MBScore:     50,
				Year:        1969,
			},
			title: "Abbey Road", artist: "The Beatles", year: 1969,
			minScore: 40, maxScore: 65,
		},
		{
			name: "completely wrong",
			candidate: Candidate{
				Title:       "Thriller",
				ArtistName:  "Michael Jackson",
				PrimaryType: "Album",
				MBScore:     30,
				Year:        1982,
			},
			title: "Abbey Road", artist: "The Beatles", year: 1969,
			minScore: 0, maxScore: 30,
		},
		{
			name: "year off by 1",
			candidate: Candidate{
				Title:       "OK Computer",
				ArtistName:  "Radiohead",
				PrimaryType: "Album",
				MBScore:     95,
				Year:        1997,
			},
			title: "OK Computer", artist: "Radiohead", year: 1998,
			minScore: 85, maxScore: 96,
		},
		{
			name: "no year available",
			candidate: Candidate{
				Title:       "OK Computer",
				ArtistName:  "Radiohead",
				PrimaryType: "Album",
				MBScore:     90,
				Year:        0,
			},
			title: "OK Computer", artist: "Radiohead", year: 1997,
			minScore: 80, maxScore: 96,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score := ScoreCandidate(tt.candidate, tt.title, tt.artist, tt.year)
			if score < tt.minScore || score > tt.maxScore {
				t.Fatalf("ScoreCandidate() = %.1f, want [%.1f, %.1f]", score, tt.minScore, tt.maxScore)
			}
		})
	}
}

func TestBestCandidate(t *testing.T) {
	candidates := []Candidate{
		{Title: "Thriller", ArtistName: "Michael Jackson", PrimaryType: "Album", MBScore: 60, Year: 1982},
		{Title: "Abbey Road", ArtistName: "The Beatles", PrimaryType: "Album", MBScore: 100, Year: 1969},
		{Title: "Let It Be", ArtistName: "The Beatles", PrimaryType: "Album", MBScore: 80, Year: 1970},
	}

	best, score, ok := BestCandidate(candidates, "Abbey Road", "The Beatles", 1969)
	if !ok {
		t.Fatal("BestCandidate returned !ok")
	}
	if best.Title != "Abbey Road" {
		t.Fatalf("BestCandidate title = %q, want %q", best.Title, "Abbey Road")
	}
	if score < 70 {
		t.Fatalf("BestCandidate score = %.1f, want >= 70", score)
	}

	// Empty candidates.
	_, _, ok = BestCandidate(nil, "x", "y", 0)
	if ok {
		t.Fatal("BestCandidate returned ok for nil candidates")
	}
}

func TestTypeBonus(t *testing.T) {
	tests := []struct {
		typ  string
		want float64
	}{
		{"Album", 10},
		{"Single", 2},
		{"Broadcast", 4},
		{"Live", 4},
		{"Remix", 4},
		{"Compilation", 4},
		{"EP", 10},
		{"", 10},
	}
	for _, tt := range tests {
		got := typeBonus(tt.typ)
		if got != tt.want {
			t.Fatalf("typeBonus(%q) = %f, want %f", tt.typ, got, tt.want)
		}
	}
}

func TestYearBonus(t *testing.T) {
	tests := []struct {
		mbYear, localYear int
		want              float64
	}{
		{1969, 1969, 4},
		{1969, 1970, 4},
		{1969, 1972, 2},
		{1969, 1975, 0},
		{0, 1969, 0},
		{1969, 0, 0},
	}
	for _, tt := range tests {
		got := yearBonus(tt.mbYear, tt.localYear)
		if got != tt.want {
			t.Fatalf("yearBonus(%d, %d) = %f, want %f", tt.mbYear, tt.localYear, got, tt.want)
		}
	}
}
