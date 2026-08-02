package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// browseKey decides both the sort order and the A-Z bucket, so the article
// strip is the whole behaviour worth pinning: without it "The …" swamps one
// letter (137 of 976 artists behind T on a real library).
func TestBrowseKey(t *testing.T) {
	cases := map[string]string{
		"The Rolling Stones": "rolling stones",
		"the beatles":        "beatles",
		"A Tribe Called Q":   "tribe called q",
		"An Emerald City":    "emerald city",
		"Theatre of Tragedy": "theatre of tragedy", // "The" only strips as a WORD
		"Anthrax":            "anthrax",
		"  Air  ":            "air",
		"":                   "",
	}
	for in, want := range cases {
		if got := browseKey(in); got != want {
			t.Fatalf("browseKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBrowseLetter(t *testing.T) {
	cases := map[string]string{
		"The Rolling Stones": "R", // the point of the whole exercise
		"Aerosmith":          "A",
		"ZZ Top":             "Z",
		"2Pac":               "#",
		"...And You Will":    "#",
		"":                   "#",
		"Édith Piaf":         "#", // non-ASCII letters bucket together rather than vanish
	}
	for in, want := range cases {
		if got := browseLetter(in); got != want {
			t.Fatalf("browseLetter(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestByLetter(t *testing.T) {
	bi := newBrowseIndex()
	bi.artists = []artistRef{
		{ID: 1, Name: "The Beatles", Letter: "B"},
		{ID: 2, Name: "Blondie", Letter: "B"},
		{ID: 3, Name: "Cream", Letter: "C"},
		{ID: 4, Name: "2Pac", Letter: "#"},
	}
	if got := len(bi.byLetter("B")); got != 2 {
		t.Fatalf("byLetter(B) = %d, want 2", got)
	}
	if got := len(bi.byLetter("#")); got != 1 {
		t.Fatalf("byLetter(#) = %d, want 1", got)
	}
	if got := len(bi.byLetter("Q")); got != 0 {
		t.Fatalf("byLetter(Q) = %d, want 0", got)
	}
	if got := len(bi.byLetter("")); got != 4 {
		t.Fatalf("byLetter(empty) = %d, want all 4", got)
	}
}

// A stale-but-present index must still answer: the browse screens are useless
// if a momentarily unreachable server empties the artist list.
func TestEnsureKeepsAServedIndexUntilItCanReplaceIt(t *testing.T) {
	bi := newBrowseIndex()
	bi.artists = []artistRef{{ID: 1, Name: "Blondie", Letter: "B"}}
	bi.base = "http://unreachable.invalid"
	// built stays zero, so ensure will try to refetch and fail.
	if err := bi.ensure("http://unreachable.invalid", false); err == nil {
		t.Fatal("ensure against an unreachable server: expected an error")
	}
	if got := len(bi.byLetter("B")); got != 1 {
		t.Fatalf("a failed refresh discarded the cached index: %d artists, want 1", got)
	}
}

func TestBrowseHandlersRejectWrongMethods(t *testing.T) {
	h := newTestController().routes()
	for _, path := range []string{"/api/letters", "/api/artists", "/api/artist", "/api/album"} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, path, nil))
		if rr.Code != http.StatusMethodNotAllowed {
			t.Fatalf("POST %s = %d, want 405", path, rr.Code)
		}
	}
}

func TestBrowseIDValidation(t *testing.T) {
	h := newTestController().routes()
	for _, q := range []string{"", "?id=", "?id=0", "?id=-3", "?id=abc"} {
		for _, path := range []string{"/api/artist", "/api/album"} {
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path+q, nil))
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("GET %s%s = %d, want 400", path, q, rr.Code)
			}
			var body struct {
				OK bool `json:"ok"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil || body.OK {
				t.Fatalf("GET %s%s: want a JSON error body, got %s", path, q, rr.Body.String())
			}
		}
	}
}
