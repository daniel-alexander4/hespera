package match

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLBTopRecordings(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Deliberately not in listen-count order, to prove TopRecordings sorts.
		_, _ = w.Write([]byte(`[
			{"recording_name":"Child in Time","total_listen_count":5000},
			{"recording_name":"Smoke on the Water","total_listen_count":900000},
			{"recording_name":"","total_listen_count":1}
		]`))
	}))
	defer srv.Close()

	c := NewLBClient(newRateLimiter(0))
	c.baseURL = srv.URL

	recs, ok := c.TopRecordings(context.Background(), "mbid-1")
	if !ok {
		t.Fatal("TopRecordings ok=false, want true")
	}
	if len(recs) != 2 {
		t.Fatalf("len = %d, want 2 (empty name dropped)", len(recs))
	}
	if recs[0].Name != "Smoke on the Water" || recs[0].ListenCount != 900000 {
		t.Fatalf("first = %+v, want the highest-count recording first", recs[0])
	}

	// Empty MBID and nil client are safe no-ops.
	if _, ok := c.TopRecordings(context.Background(), ""); ok {
		t.Fatal("empty MBID returned ok")
	}
	var nilC *LBClient
	if _, ok := nilC.TopRecordings(context.Background(), "mbid-1"); ok {
		t.Fatal("nil client returned ok")
	}
}

func TestLBSimilarArtists(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("algorithm") == "" {
			t.Errorf("similar-artists request missing required algorithm param: %s", r.URL.RawQuery)
		}
		// Deliberately not score-ordered, and one bad row, to prove sort + filter.
		_, _ = w.Write([]byte(`[
			{"artist_mbid":"mb-nirvana","name":"Nirvana","comment":"grunge","score":800},
			{"artist_mbid":"mb-muse","name":"Muse","comment":"","score":11000},
			{"artist_mbid":"","name":"NoMBID","score":99},
			{"artist_mbid":"mb-x","name":"","score":99}
		]`))
	}))
	defer srv.Close()

	c := NewLBClient(newRateLimiter(0))
	c.labsURL = srv.URL

	got, ok := c.SimilarArtists(context.Background(), "mb-ref")
	if !ok {
		t.Fatal("SimilarArtists ok=false, want true")
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2 (rows missing mbid/name dropped)", len(got))
	}
	if got[0].Name != "Muse" || got[0].Score != 11000 {
		t.Fatalf("first = %+v, want the highest-score artist first", got[0])
	}
	if got[1].Comment != "grunge" {
		t.Fatalf("disambiguation comment not carried: %+v", got[1])
	}

	if _, ok := c.SimilarArtists(context.Background(), ""); ok {
		t.Fatal("empty MBID returned ok")
	}
	var nilC *LBClient
	if _, ok := nilC.SimilarArtists(context.Background(), "mb-ref"); ok {
		t.Fatal("nil client returned ok")
	}
}

func TestLBTopRecordingsHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := NewLBClient(newRateLimiter(0))
	c.baseURL = srv.URL
	if _, ok := c.TopRecordings(context.Background(), "mbid-1"); ok {
		t.Fatal("404 should be a miss (ok=false), not a match")
	}
}
