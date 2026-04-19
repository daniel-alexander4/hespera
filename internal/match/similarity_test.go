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
