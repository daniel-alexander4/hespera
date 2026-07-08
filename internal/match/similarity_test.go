package match

import (
	"math"
	"testing"
)

func TestNormalize(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"Hello World", "hello world"},
		{"AC/DC", "ac dc"},
		{"  Guns  N'  Roses  ", "guns n roses"},
		{"Beyoncé", "beyoncé"},
		{"100% Pure", "100 pure"},
		{"", ""},
		{"---", ""},
	}
	for _, tt := range tests {
		got := Normalize(tt.in)
		if got != tt.want {
			t.Fatalf("Normalize(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestLevenshteinDistance(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"abc", "", 3},
		{"", "abc", 3},
		{"abc", "abc", 0},
		{"kitten", "sitting", 3},
		{"saturday", "sunday", 3},
	}
	for _, tt := range tests {
		got := LevenshteinDistance(tt.a, tt.b)
		if got != tt.want {
			t.Fatalf("LevenshteinDistance(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestNormalizedSimilarity(t *testing.T) {
	tests := []struct {
		a, b   string
		minSim float64
		maxSim float64
	}{
		{"Abbey Road", "Abbey Road", 1.0, 1.0},
		{"abbey road", "ABBEY ROAD", 1.0, 1.0},
		{"Abbey Road", "Abby Road", 0.8, 1.0},
		{"Completely Different", "Totally Other", 0.0, 0.4},
		{"", "", 1.0, 1.0},
	}
	for _, tt := range tests {
		got := NormalizedSimilarity(tt.a, tt.b)
		if got < tt.minSim-1e-9 || got > tt.maxSim+1e-9 {
			t.Fatalf("NormalizedSimilarity(%q, %q) = %f, want [%f, %f]",
				tt.a, tt.b, got, tt.minSim, tt.maxSim)
		}
		if got < 0 || got > 1.0+1e-9 {
			t.Fatalf("NormalizedSimilarity(%q, %q) = %f, out of [0,1]", tt.a, tt.b, got)
		}
	}

	// Symmetry check.
	sim1 := NormalizedSimilarity("foo bar", "bar foo")
	sim2 := NormalizedSimilarity("bar foo", "foo bar")
	if math.Abs(sim1-sim2) > 1e-9 {
		t.Fatalf("NormalizedSimilarity not symmetric: %f vs %f", sim1, sim2)
	}
}

func TestTitleMatchSimilarity(t *testing.T) {
	tests := []struct {
		name           string
		candidate      string // TMDB canonical name
		query          string // library folder name
		minSim, maxSim float64
	}{
		// Rescued: query is a whole-word subset of the canonical name.
		{"leading article", "The IT Crowd", "IT Crowd", 0.90, 0.91},
		{"franchise prefix", "Tom Clancy's Jack Ryan", "Jack Ryan", 0.90, 0.91},
		{"subtitle suffix", "Pennyworth: The Origin of Batman's Butler", "Pennyworth", 0.90, 0.91},
		// Exact / near-exact keep their whole-string score (no boost needed).
		{"exact", "Severance", "Severance", 1.0, 1.0},
		{"typo above threshold", "The Mighty Boosh", "The Might Boosh", 0.90, 1.0},
		// FP guard: generic single short words stay on whole-string scoring.
		{"short single word gated", "House of Cards", "House", 0.0, 0.40},
		{"6-char single word gated", "Doctor Who", "Doctor", 0.50, 0.65},
		// Word-boundary: a substring that isn't a whole word is not a containment.
		{"not a whole word", "The Terminator", "Terminal", 0.40, 0.60},
		// Unrelated titles unchanged.
		{"unrelated", "Better Call Saul", "Breaking Bad", 0.0, 0.40},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TitleMatchSimilarity(tt.candidate, tt.query)
			if got < tt.minSim-1e-9 || got > tt.maxSim+1e-9 {
				t.Fatalf("TitleMatchSimilarity(%q, %q) = %f, want [%f, %f]",
					tt.candidate, tt.query, got, tt.minSim, tt.maxSim)
			}
		})
	}

	// The boost must never change a music-scoring result: TitleMatchSimilarity
	// only ever raises a score, and only via containment.
	for _, p := range [][2]string{{"House of Cards", "House"}, {"The Terminator", "Terminal"}} {
		if TitleMatchSimilarity(p[0], p[1]) != NormalizedSimilarity(p[0], p[1]) {
			t.Fatalf("gated case %q/%q should equal NormalizedSimilarity", p[0], p[1])
		}
	}
}
