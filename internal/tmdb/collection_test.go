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

// TestFetchMovieCollection: the backfill refreshes the movie blob (pre-field
// cached copies lack belongs_to_collection), caches the collection parts, and
// writes the marker — including for standalone films, so a page view enqueues
// the job at most once either way.
func TestFetchMovieCollection(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	mux := http.NewServeMux()
	// Movie 10: part of a collection.
	mux.HandleFunc("/3/movie/10", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":10,"title":"Franchise Film","release_date":"1999-01-01",
			"belongs_to_collection":{"id":500,"name":"The Franchise Collection","poster_path":"/col.jpg"}}`)
	})
	mux.HandleFunc("/3/collection/500", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":500,"name":"The Franchise Collection","parts":[
			{"id":10,"title":"Franchise Film","poster_path":"/a.jpg","release_date":"1999-01-01"},
			{"id":11,"title":"Franchise Film II","poster_path":"/b.jpg","release_date":"2003-01-01"}
		]}`)
	})
	// Movie 20: standalone (belongs_to_collection null).
	mux.HandleFunc("/3/movie/20", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":20,"title":"Standalone","belongs_to_collection":null}`)
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

	// Seed a stale movie blob without the collection field (pre-field cache).
	if _, err := db.Exec(
		"INSERT INTO movie_metadata_cache(entity_key,lang,payload_json,fetched_at) VALUES('movie:10','en','{\"id\":10,\"title\":\"Franchise Film\"}',datetime('now'))"); err != nil {
		t.Fatal(err)
	}

	if err := m.FetchMovieCollection(ctx, 10); err != nil {
		t.Fatalf("FetchMovieCollection(10): %v", err)
	}

	// Movie blob refreshed to carry belongs_to_collection.
	var blob string
	if err := db.QueryRow("SELECT payload_json FROM movie_metadata_cache WHERE entity_key='movie:10'").Scan(&blob); err != nil {
		t.Fatal(err)
	}
	var movie Movie
	if err := json.Unmarshal([]byte(blob), &movie); err != nil {
		t.Fatal(err)
	}
	if movie.BelongsToCollection == nil || movie.BelongsToCollection.ID != 500 || movie.BelongsToCollection.Name != "The Franchise Collection" {
		t.Errorf("refreshed blob collection = %+v", movie.BelongsToCollection)
	}

	// Collection parts cached.
	if err := db.QueryRow("SELECT payload_json FROM movie_metadata_cache WHERE entity_key='collection:500'").Scan(&blob); err != nil {
		t.Fatalf("collection blob: %v", err)
	}
	var parts []RelatedTitle
	if err := json.Unmarshal([]byte(blob), &parts); err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 || parts[1].ID != 11 || parts[1].Year() != "2003" {
		t.Errorf("parts = %+v", parts)
	}

	// Marker written.
	var x int
	if err := db.QueryRow("SELECT 1 FROM movie_metadata_cache WHERE entity_key='movie:10:collection'").Scan(&x); err != nil {
		t.Error("collection marker missing for movie 10")
	}

	// Standalone film: marker written, no collection blob.
	if err := m.FetchMovieCollection(ctx, 20); err != nil {
		t.Fatalf("FetchMovieCollection(20): %v", err)
	}
	if err := db.QueryRow("SELECT 1 FROM movie_metadata_cache WHERE entity_key='movie:20:collection'").Scan(&x); err != nil {
		t.Error("collection marker missing for standalone movie 20")
	}
}
