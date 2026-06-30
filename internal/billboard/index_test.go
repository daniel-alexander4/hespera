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

func TestWeeklyCharts1968(t *testing.T) {
	weeks := WeeklyCharts(1968)
	if len(weeks) < 50 || len(weeks) > 53 {
		t.Fatalf("1968 has %d weekly charts, want ~52", len(weeks))
	}
	// Weeks are chronological.
	for i := 1; i < len(weeks); i++ {
		if weeks[i-1].Date >= weeks[i].Date {
			t.Fatalf("weeks not chronological at %d: %q then %q", i, weeks[i-1].Date, weeks[i].Date)
		}
	}
	for _, w := range weeks {
		if len(w.Date) != 10 {
			t.Fatalf("bad chart date %q", w.Date)
		}
		if len(w.Entries) == 0 {
			t.Fatalf("week %s has no entries", w.Date)
		}
		// Entries ordered by ascending position; a #1 leads each chart.
		if w.Entries[0].Pos != 1 {
			t.Fatalf("week %s first entry pos = %d, want 1", w.Date, w.Entries[0].Pos)
		}
		for i := 1; i < len(w.Entries); i++ {
			if w.Entries[i-1].Pos > w.Entries[i].Pos {
				t.Fatalf("week %s entries not sorted by pos", w.Date)
			}
		}
		for _, e := range w.Entries {
			if e.Title == "" || e.Artist == "" || e.Pos <= 0 {
				t.Fatalf("malformed entry in %s: %+v", w.Date, e)
			}
		}
	}
}

func TestWeeklyChartsMiss(t *testing.T) {
	if got := WeeklyCharts(1900); got != nil {
		t.Fatalf("WeeklyCharts(1900) = %v, want nil", got)
	}
}
