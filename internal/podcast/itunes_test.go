package podcast

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// stubDirectory points the fixed provider URL at a test server and returns a
// client whose address policy is relaxed — httptest always binds loopback,
// which the real dialer refuses by design (see TestRealClientRefusesLoopback).
func stubDirectory(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	prev := itunesSearchURL
	itunesSearchURL = srv.URL
	t.Cleanup(func() { itunesSearchURL = prev })

	return permissiveClient(t)
}

const itunesBody = `{"resultCount":3,"results":[
  {"collectionName":"Good Show","artistName":"A Person","feedUrl":"https://example.com/good.xml",
   "artworkUrl600":"https://example.com/600.jpg","artworkUrl100":"https://example.com/100.jpg",
   "trackCount":412,"genres":["Society & Culture","Podcasts"]},
  {"collectionName":"No Feed Here","artistName":"Nobody","feedUrl":"","artworkUrl100":"https://example.com/x.jpg"},
  {"collectionName":"Small Art Only","artistName":"B","feedUrl":"https://example.com/small.xml",
   "artworkUrl100":"https://example.com/small100.jpg","trackCount":7}
]}`

func TestSearchDirectory(t *testing.T) {
	var gotQuery string
	c := stubDirectory(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(itunesBody))
	})

	res, err := c.SearchDirectory(context.Background(), "  good show  ")
	if err != nil {
		t.Fatalf("SearchDirectory: %v", err)
	}

	if !strings.Contains(gotQuery, "media=podcast") {
		t.Errorf("the search must be scoped to podcasts, got %q", gotQuery)
	}
	if !strings.Contains(gotQuery, "term=good+show") {
		t.Errorf("term not sent trimmed: %q", gotQuery)
	}

	// The feed-less row is dropped: a Subscribe button that cannot work is
	// worse than an absent row.
	if len(res) != 2 {
		var names []string
		for _, r := range res {
			names = append(names, r.Name)
		}
		t.Fatalf("want 2 usable results, got %d: %v", len(res), names)
	}

	first := res[0]
	if first.Name != "Good Show" || first.Author != "A Person" || first.Episodes != 412 {
		t.Errorf("row: %+v", first)
	}
	if first.ImageURL != "https://example.com/600.jpg" {
		t.Errorf("the larger artwork should win: %q", first.ImageURL)
	}
	if first.Genre != "Society & Culture" {
		t.Errorf("genre: %q", first.Genre)
	}
	// Falls back to the small artwork when there is no 600px version.
	if res[1].ImageURL != "https://example.com/small100.jpg" {
		t.Errorf("artwork fallback: %q", res[1].ImageURL)
	}
}

// TestSearchDirectoryValidatesFeedURLs: the feed address is third-party data
// even when it arrives from a trusted directory, and this is the one place it
// enters the system.
func TestSearchDirectoryValidatesFeedURLs(t *testing.T) {
	c := stubDirectory(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"results":[
		  {"collectionName":"Hostile","artistName":"x","feedUrl":"file:///etc/passwd"},
		  {"collectionName":"Also Hostile","artistName":"x","feedUrl":"javascript:alert(1)"},
		  {"collectionName":"Fine","artistName":"x","feedUrl":"https://example.com/ok.xml"}
		]}`))
	})

	res, err := c.SearchDirectory(context.Background(), "anything")
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Name != "Fine" {
		t.Fatalf("non-http feed URLs must be dropped, got %+v", res)
	}
}

func TestSearchDirectoryRejectsEmptyTerm(t *testing.T) {
	c := stubDirectory(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("an empty search should not reach the network")
	})
	if _, err := c.SearchDirectory(context.Background(), "   "); err == nil {
		t.Fatal("an empty term was accepted")
	}
}

func TestSearchDirectoryHandlesJunk(t *testing.T) {
	c := stubDirectory(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html>not json</html>`))
	})
	if _, err := c.SearchDirectory(context.Background(), "x"); err == nil {
		t.Fatal("a non-JSON response was accepted")
	}
}

func TestSearchDirectorySurfacesUpstreamFailure(t *testing.T) {
	c := stubDirectory(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusServiceUnavailable)
	})
	_, err := c.SearchDirectory(context.Background(), "x")
	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("want the upstream status surfaced, got %v", err)
	}
}
