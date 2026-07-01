package web

import (
	"os"
	"path/filepath"
	"testing"

	"hespera/internal/video"
)

func TestClassifyChapter(t *testing.T) {
	cases := []struct {
		title string
		kind  string
		ok    bool
	}{
		{"Intro", "intro", true},
		{"Opening Credits", "intro", true},
		{"Title Sequence", "intro", true},
		{"Main Title", "intro", true},
		{"Previously On", "recap", true},
		{"Recap", "recap", true},
		{"Advertisement", "commercial", true},
		{"Commercials", "commercial", true},
		{"Ad Break", "commercial", true},
		{"Chapter 1", "", false},
		{"Chapter 2", "", false},
		{"Scene 4", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		kind, ok := classifyChapter(c.title)
		if ok != c.ok || kind != c.kind {
			t.Errorf("classifyChapter(%q) = (%q,%v), want (%q,%v)", c.title, kind, ok, c.kind, c.ok)
		}
	}
}

func TestReadEDLSegments(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.edl")
	// 50-58 commercial (action 0); 10-20 commercial (no action → 0); 90-100 action 3
	// (commercial break); 30-40 action 1 (mute, ignored); 5-5 zero length (ignored);
	// "bad" non-numeric (ignored); 100-90 reversed (ignored).
	content := "50.00\t58.00\t0\n10 20\n90 100 3\n30 40 1\n5 5 0\nbad line here\n100 90 0\n"
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	got := readEDLSegments(p)
	want := []skipSegment{
		{50, 58, "commercial"},
		{10, 20, "commercial"},
		{90, 100, "commercial"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d segments, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("segment %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if readEDLSegments(filepath.Join(dir, "missing.edl")) != nil {
		t.Error("missing EDL sidecar should yield nil")
	}
}

func TestSkipSegmentsForCombinesChaptersAndEDL(t *testing.T) {
	dir := t.TempDir()
	media := filepath.Join(dir, "ep.mkv")
	if err := os.WriteFile(filepath.Join(dir, "ep.edl"), []byte("50 58 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	probe := &video.ProbeResult{Chapters: []video.ProbeChapter{
		{StartSec: 0, EndSec: 8, Title: "Intro"},
		{StartSec: 8, EndSec: 40, Title: "Chapter 1"}, // not skippable
		{StartSec: 40, EndSec: 48, Title: "Advertisement"},
	}}
	got := skipSegmentsFor(probe, media)
	want := []skipSegment{
		{0, 8, "intro"},
		{40, 48, "commercial"},
		{50, 58, "commercial"}, // from the EDL sidecar
	}
	if len(got) != len(want) {
		t.Fatalf("got %d segments, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("segment %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestSkipSegmentsForNoMarkers(t *testing.T) {
	// A plain file (no skippable chapters, no sidecar) yields nothing — the common
	// case for the current library; the player must show no Skip button.
	probe := &video.ProbeResult{Chapters: []video.ProbeChapter{{StartSec: 0, EndSec: 60, Title: "Chapter 1"}}}
	if got := skipSegmentsFor(probe, filepath.Join(t.TempDir(), "none.mkv")); len(got) != 0 {
		t.Fatalf("expected no segments, got %+v", got)
	}
}
