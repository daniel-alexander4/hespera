package match

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"hespera/internal/ratelimit"
)

func TestLastfmTopTracks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("method") != "artist.gettoptracks" {
			t.Errorf("wrong method param: %s", r.URL.RawQuery)
		}
		if r.URL.Query().Get("api_key") != "k" {
			t.Errorf("missing api_key: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"toptracks":{"track":[
			{"name":"Hey Jude","playcount":"12345"},
			{"name":"Let It Be","playcount":"6000"},
			{"name":"","playcount":"1"}
		]}}`))
	}))
	defer srv.Close()

	c := NewLastfmClient("k")
	c.baseURL = srv.URL
	c.limiter = ratelimit.New(0)

	got, ok := c.TopTracks(context.Background(), "The Beatles")
	if !ok {
		t.Fatal("TopTracks ok=false, want true")
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2 (empty name dropped)", len(got))
	}
	if got[NormalizeForDedup("Hey Jude")] != 12345 {
		t.Fatalf("Hey Jude playcount = %d, want 12345", got[NormalizeForDedup("Hey Jude")])
	}
	if got[NormalizeForDedup("Let It Be")] != 6000 {
		t.Fatalf("Let It Be playcount = %d, want 6000", got[NormalizeForDedup("Let It Be")])
	}
}

func TestLastfmInert(t *testing.T) {
	if NewLastfmClient("") != nil {
		t.Fatal("NewLastfmClient(\"\") should be nil (no key → no blend)")
	}
	var nilC *LastfmClient
	if _, ok := nilC.TopTracks(context.Background(), "x"); ok {
		t.Fatal("nil client returned ok")
	}
	c := NewLastfmClient("k")
	if _, ok := c.TopTracks(context.Background(), ""); ok {
		t.Fatal("empty artist returned ok")
	}
}
