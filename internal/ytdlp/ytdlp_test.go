package ytdlp

import (
	"context"
	"errors"
	"testing"
)

func TestParseIDs(t *testing.T) {
	out := []byte("dQw4w9WgXcQ\nfHiGbolFFGw\n\nnot a valid id line\nSFY5ZRmMEjM\n")
	got := parseIDs(out, 5)
	want := []string{"dQw4w9WgXcQ", "fHiGbolFFGw", "SFY5ZRmMEjM"}
	if len(got) != len(want) {
		t.Fatalf("parseIDs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("parseIDs[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestParseIDsCapsAtN(t *testing.T) {
	out := []byte("dQw4w9WgXcQ\nfHiGbolFFGw\nSFY5ZRmMEjM\n")
	if got := parseIDs(out, 2); len(got) != 2 {
		t.Fatalf("parseIDs capped = %v, want 2", got)
	}
}

func TestSearchUsesSeamAndBuildsQuery(t *testing.T) {
	var gotQuery string
	var gotN int
	orig := runSearch
	runSearch = func(_ context.Context, q string, n int) ([]byte, error) {
		gotQuery, gotN = q, n
		return []byte("dQw4w9WgXcQ\n"), nil
	}
	defer func() { runSearch = orig }()

	ids, err := Search(context.Background(), "Rick Astley", "Never Gonna Give You Up", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(ids) != 1 || ids[0] != "dQw4w9WgXcQ" {
		t.Fatalf("ids = %v", ids)
	}
	if gotQuery != "Rick Astley Never Gonna Give You Up" {
		t.Fatalf("query = %q", gotQuery)
	}
	if gotN != 5 {
		t.Fatalf("n = %d, want 5", gotN)
	}
}

func TestSearchEmptyQueryNoRun(t *testing.T) {
	orig := runSearch
	ran := false
	runSearch = func(context.Context, string, int) ([]byte, error) { ran = true; return nil, nil }
	defer func() { runSearch = orig }()

	ids, err := Search(context.Background(), "", "", 5)
	if err != nil || len(ids) != 0 {
		t.Fatalf("empty query = (%v,%v), want (nil,nil)", ids, err)
	}
	if ran {
		t.Fatal("empty query must not invoke yt-dlp")
	}
}

func TestSearchToolErrorPropagates(t *testing.T) {
	orig := runSearch
	runSearch = func(context.Context, string, int) ([]byte, error) { return nil, errors.New("boom") }
	defer func() { runSearch = orig }()

	if _, err := Search(context.Background(), "a", "b", 5); err == nil {
		t.Fatal("a tool error must propagate so the caller treats it as unavailable")
	}
}
