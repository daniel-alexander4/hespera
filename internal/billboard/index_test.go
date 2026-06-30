package billboard

import (
	"os"
	"path/filepath"
	"testing"
)

// fixtureCSV is a tiny fabricated Hot 100 archive (NOT real chart data) — two
// 1968 weeks — used to exercise BuildIndex + the disk loader without shipping or
// fetching any real dataset.
const fixtureCSV = `chart_date,current_position,title,performer,previous_position,peak_position,weeks_on_chart
1968-09-28,2,Harper Valley P.T.A.,Jeannie C. Riley,1,1,8
1968-09-28,1,Hey Jude,The Beatles,3,1,3
1968-10-05,1,Hey Jude,The Beatles,1,1,4
1968-10-05,2,Fire,The Crazy World of Arthur Brown,4,2,6
`

// buildFixture writes the fixture archive and runs BuildIndex into a fresh
// DataDir, returning that dir.
func buildFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	csvPath := filepath.Join(dir, "fixture.csv")
	if err := os.WriteFile(csvPath, []byte(fixtureCSV), 0o644); err != nil {
		t.Fatalf("write fixture csv: %v", err)
	}
	if err := BuildIndex(dir, csvPath); err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}
	return dir
}

func TestBuildAndYears(t *testing.T) {
	dir := buildFixture(t)
	min, max, ok := Years(dir)
	if !ok || min != 1968 || max != 1968 {
		t.Fatalf("Years = %d..%d ok=%v, want 1968..1968 true", min, max, ok)
	}
}

func TestWeeklyChartsStructure(t *testing.T) {
	dir := buildFixture(t)
	weeks := WeeklyCharts(dir, 1968)
	if len(weeks) != 2 {
		t.Fatalf("1968 has %d weekly charts, want 2", len(weeks))
	}
	if weeks[0].Date != "1968-09-28" || weeks[1].Date != "1968-10-05" {
		t.Fatalf("weeks not chronological: %q then %q", weeks[0].Date, weeks[1].Date)
	}
	for _, w := range weeks {
		if len(w.Date) != 10 || len(w.Entries) == 0 {
			t.Fatalf("bad week %+v", w)
		}
		// Entries ordered by ascending position; the #1 leads each chart.
		if w.Entries[0].Pos != 1 || w.Entries[0].Title != "Hey Jude" {
			t.Fatalf("week %s first entry = %+v, want #1 Hey Jude", w.Date, w.Entries[0])
		}
		for i := 1; i < len(w.Entries); i++ {
			if w.Entries[i-1].Pos > w.Entries[i].Pos {
				t.Fatalf("week %s entries not sorted by pos", w.Date)
			}
		}
	}
}

func TestWeeklyChartsMiss(t *testing.T) {
	dir := buildFixture(t)
	if got := WeeklyCharts(dir, 1900); got != nil {
		t.Fatalf("WeeklyCharts(1900) = %v, want nil", got)
	}
}

// TestAbsentDataset confirms the feature reports "no data" (rather than failing)
// when nothing has been fetched into the DataDir — the off / not-yet-fetched
// state.
func TestAbsentDataset(t *testing.T) {
	dir := t.TempDir()
	if _, _, ok := Years(dir); ok {
		t.Fatal("Years should be ok=false when the dataset is absent")
	}
	if got := WeeklyCharts(dir, 1968); got != nil {
		t.Fatalf("WeeklyCharts on absent dataset = %v, want nil", got)
	}
}
