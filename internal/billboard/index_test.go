package billboard

import "testing"

func TestYearsCovered(t *testing.T) {
	min, max, ok := Years()
	if !ok {
		t.Fatal("dataset failed to load")
	}
	if min > 1958 || max < 1968 {
		t.Fatalf("unexpected year range %d..%d", min, max)
	}
}

func TestYear1968(t *testing.T) {
	acts := Year(1968)
	if len(acts) < 200 {
		t.Fatalf("1968 has %d artists, want >=200", len(acts))
	}
	// Ordered by peak ascending — the first act must be a #1.
	if acts[0].Peak != 1 {
		t.Fatalf("first 1968 act peak = %d, want 1", acts[0].Peak)
	}
	// Spot-check a known 1968 chart-topper and its song data.
	var beatles *Artist
	for i := range acts {
		if acts[i].Name == "The Beatles" {
			beatles = &acts[i]
			break
		}
	}
	if beatles == nil {
		t.Fatal("The Beatles not found in 1968")
	}
	if beatles.Peak != 1 {
		t.Fatalf("Beatles 1968 peak = %d, want 1", beatles.Peak)
	}
	if len(beatles.Songs) == 0 {
		t.Fatal("Beatles have no songs")
	}
	for _, s := range beatles.Songs {
		if s.Title == "" || s.Peak <= 0 || len(s.Debut) != 10 {
			t.Fatalf("malformed song: %+v", s)
		}
	}
	// Songs sorted by peak ascending.
	for i := 1; i < len(beatles.Songs); i++ {
		if beatles.Songs[i-1].Peak > beatles.Songs[i].Peak {
			t.Fatalf("songs not sorted by peak: %+v", beatles.Songs)
		}
	}
}

func TestYearMiss(t *testing.T) {
	if got := Year(1900); got != nil {
		t.Fatalf("Year(1900) = %v, want nil", got)
	}
}
