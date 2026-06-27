package match

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestSearchArtistCandidates verifies the artist search surfaces the full
// candidate set with the human-disambiguation fields (disambiguation, type,
// country, life span) — the data a user needs to pick the right same-named
// artist, e.g. the two "Jimmie Rodgers" entries.
func TestSearchArtistCandidates(t *testing.T) {
	const body = `{
	  "artists": [
	    {
	      "id": "394492c0-cecf-40a8-b676-0e5706317fab",
	      "name": "Jimmie Rodgers",
	      "disambiguation": "\"The Singing Brakeman\"",
	      "type": "Person",
	      "country": "US",
	      "life-span": {"begin": "1897-09-08", "end": "1933-05-26"},
	      "score": 100
	    },
	    {
	      "id": "d5bc8537-5bc5-4e37-915a-008c88436092",
	      "name": "Jimmie Rodgers",
	      "disambiguation": "Honeycomb / Kisses Sweeter Than Wine",
	      "type": "Person",
	      "country": "US",
	      "life-span": {"begin": "1933-09-18", "end": "2021-01-18"},
	      "score": 99
	    }
	  ]
	}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c := &MBClient{
		client:  srv.Client(),
		baseURL: srv.URL,
		limiter: newRateLimiter(0),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	got, err := c.SearchArtistCandidates(ctx, "Jimmie Rodgers")
	if err != nil {
		t.Fatalf("SearchArtistCandidates: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d candidates, want 2", len(got))
	}

	want0 := ArtistCandidate{
		MBID:           "394492c0-cecf-40a8-b676-0e5706317fab",
		Name:           "Jimmie Rodgers",
		Disambiguation: `"The Singing Brakeman"`,
		Type:           "Person",
		Country:        "US",
		BeginDate:      "1897-09-08",
		EndDate:        "1933-05-26",
		Score:          100,
	}
	if got[0] != want0 {
		t.Fatalf("candidate[0] = %+v, want %+v", got[0], want0)
	}

	// The pop singer (the library's actual artist) must be present as a separate
	// candidate so the user can pick it over the top-scored country legend.
	if got[1].MBID != "d5bc8537-5bc5-4e37-915a-008c88436092" || got[1].EndDate != "2021-01-18" {
		t.Fatalf("candidate[1] = %+v, want the 1933–2021 pop singer", got[1])
	}
}

func TestSearchArtistCandidatesEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"artists": []}`))
	}))
	defer srv.Close()

	c := &MBClient{client: srv.Client(), baseURL: srv.URL, limiter: newRateLimiter(0)}
	got, err := c.SearchArtistCandidates(context.Background(), "Nobody")
	if err != nil {
		t.Fatalf("SearchArtistCandidates: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d candidates, want 0", len(got))
	}
}
