package tmdb

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestFetchRelated verifies the recommendations-first / similar-fallback
// cascade, the relatedLimit cap, and the blob caching (empty result caches []
// so the blob doubles as the fetched marker).
func TestFetchRelated(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	recHits, simHits := 0, 0
	mux := http.NewServeMux()
	// Show 100: recommendations has data (16 titles → capped at relatedLimit).
	mux.HandleFunc("/3/tv/100/recommendations", func(w http.ResponseWriter, r *http.Request) {
		recHits++
		w.Header().Set("Content-Type", "application/json")
		items := ""
		for i := 1; i <= 16; i++ {
			if i > 1 {
				items += ","
			}
			items += fmt.Sprintf(`{"id":%d,"name":"Show %d","poster_path":"/p%d.jpg","first_air_date":"200%d-01-01"}`, i, i, i, i%10)
		}
		fmt.Fprintf(w, `{"results":[%s]}`, items)
	})
	// Show 200: recommendations empty → falls back to similar.
	mux.HandleFunc("/3/tv/200/recommendations", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"results":[]}`)
	})
	mux.HandleFunc("/3/tv/200/similar", func(w http.ResponseWriter, r *http.Request) {
		simHits++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"results":[{"id":42,"name":"Similar Show","poster_path":"/s.jpg","first_air_date":"1999-09-09"}]}`)
	})
	// Movie 300: nothing anywhere → caches [].
	mux.HandleFunc("/3/movie/300/recommendations", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"results":[]}`)
	})
	mux.HandleFunc("/3/movie/300/similar", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"results":[]}`)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ch := make(chan time.Time)
	close(ch)
	m := &Matcher{
		db:     db,
		client: &Client{apiKey: "k", httpClient: srv.Client(), apiBase: srv.URL + "/3", limiter: ch},
	}

	// Recommendations-first, capped.
	if err := m.FetchTVSimilar(ctx, 100); err != nil {
		t.Fatalf("FetchTVSimilar(100): %v", err)
	}
	var blob string
	if err := db.QueryRow("SELECT payload_json FROM tv_series_metadata_cache WHERE entity_key='show:100:similar' AND lang='en'").Scan(&blob); err != nil {
		t.Fatalf("blob 100: %v", err)
	}
	var list []RelatedTitle
	if err := json.Unmarshal([]byte(blob), &list); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(list) != relatedLimit {
		t.Errorf("len = %d, want capped at %d", len(list), relatedLimit)
	}
	if list[0].DisplayTitle() != "Show 1" || list[0].Year() != "2001" {
		t.Errorf("first = %q/%q", list[0].DisplayTitle(), list[0].Year())
	}
	if recHits != 1 || simHits != 0 {
		t.Errorf("hits = rec %d / sim %d, want 1/0", recHits, simHits)
	}

	// Fallback to similar on empty recommendations.
	if err := m.FetchTVSimilar(ctx, 200); err != nil {
		t.Fatalf("FetchTVSimilar(200): %v", err)
	}
	if err := db.QueryRow("SELECT payload_json FROM tv_series_metadata_cache WHERE entity_key='show:200:similar' AND lang='en'").Scan(&blob); err != nil {
		t.Fatalf("blob 200: %v", err)
	}
	if simHits != 1 {
		t.Errorf("similar fallback not used (simHits=%d)", simHits)
	}
	list = nil
	_ = json.Unmarshal([]byte(blob), &list)
	if len(list) != 1 || list[0].ID != 42 {
		t.Errorf("fallback list = %+v", list)
	}

	// Nothing anywhere → [] cached (the marker), not an error, not absence.
	if err := m.FetchMovieSimilar(ctx, 300); err != nil {
		t.Fatalf("FetchMovieSimilar(300): %v", err)
	}
	if err := db.QueryRow("SELECT payload_json FROM movie_metadata_cache WHERE entity_key='movie:300:similar'").Scan(&blob); err != nil {
		t.Fatalf("blob 300: %v", err)
	}
	if blob != "[]" {
		t.Errorf("empty blob = %q, want []", blob)
	}
}
