package itunes

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSearchParsesAndUpsizesArtwork(t *testing.T) {
	var gotTerm, gotEntity string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTerm = r.URL.Query().Get("term")
		gotEntity = r.URL.Query().Get("entity")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"resultCount":1,"results":[{"artworkUrl100":"https://is1.mzstatic.com/image/a/100x100bb.jpg"}]}`))
	}))
	defer srv.Close()

	c := &Client{http: srv.Client(), apiURL: srv.URL}
	got, err := c.Search(context.Background(), "The Beatles", "Hey Jude")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if want := "https://is1.mzstatic.com/image/a/600x600bb.jpg"; got != want {
		t.Fatalf("art url = %q, want %q (upsized)", got, want)
	}
	if gotTerm != "The Beatles Hey Jude" {
		t.Fatalf("term = %q", gotTerm)
	}
	if gotEntity != "song" {
		t.Fatalf("entity = %q, want song", gotEntity)
	}
}

func TestSearchEmptyResultsIsCachedMiss(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"resultCount":0,"results":[]}`))
	}))
	defer srv.Close()
	c := &Client{http: srv.Client(), apiURL: srv.URL}
	got, err := c.Search(context.Background(), "x", "y")
	if got != "" || err != nil {
		t.Fatalf("no-match should yield (\"\",nil), got (%q,%v)", got, err)
	}
}

func TestSearchRateLimitedIsSentinel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	c := &Client{http: srv.Client(), apiURL: srv.URL}
	_, err := c.Search(context.Background(), "x", "y")
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("403 should be ErrRateLimited, got %v", err)
	}
}

func TestSearchEmptyTitleNoOp(t *testing.T) {
	c := New()
	got, err := c.Search(context.Background(), "artist", "")
	if got != "" || err != nil {
		t.Fatalf("empty title = (%q,%v), want (\"\",nil)", got, err)
	}
	// nil client is also a no-op, not a panic.
	if got, err := (*Client)(nil).Search(context.Background(), "a", "b"); got != "" || err != nil {
		t.Fatalf("nil Search = (%q,%v)", got, err)
	}
}
