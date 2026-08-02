package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
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

// With nothing to serve, ensure has no choice but to block and report failure.
func TestEnsureFailsWhenItHasNothingToServe(t *testing.T) {
	bi := newBrowseIndex()
	if err := bi.ensure("http://127.0.0.1:1", false); err == nil {
		t.Fatal("ensure with no index and an unreachable server: expected an error")
	}
}

// With an index in hand it must answer immediately even when stale and even
// when the server is unreachable — the build takes ~22s on the hardware this
// runs on, so blocking a letter tap on a refresh (or failing one because the
// server blinked) is worse than serving a ten-minute-old artist list.
func TestEnsureServesStaleRatherThanBlockingOrFailing(t *testing.T) {
	bi := newBrowseIndex()
	bi.artists = []artistRef{{ID: 1, Name: "Blondie", Letter: "B"}}
	bi.base = "http://127.0.0.1:1"
	// built stays zero, so the index counts as stale and a refresh is due.

	done := make(chan error, 1)
	go func() { done <- bi.ensure("http://127.0.0.1:1", false) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("stale-but-present index returned an error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ensure blocked on a refresh instead of serving what it had")
	}

	if got := len(bi.byLetter("B")); got != 1 {
		t.Fatalf("a failed background refresh discarded the cached index: %d artists, want 1", got)
	}
}

// A different server must NOT be served the previous one's artists, so that
// case blocks and rebuilds even though an index exists.
func TestEnsureRebuildsWhenTheServerChanges(t *testing.T) {
	bi := newBrowseIndex()
	bi.artists = []artistRef{{ID: 1, Name: "Blondie", Letter: "B"}}
	bi.base = "http://old.invalid"
	bi.built = time.Now() // fresh — only the base differs
	if err := bi.ensure("http://127.0.0.1:1", false); err == nil {
		t.Fatal("pointing at a new server: expected a blocking rebuild, and an error when it fails")
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
