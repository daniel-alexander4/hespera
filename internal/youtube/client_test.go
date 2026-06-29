package youtube

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewNilWithoutKey(t *testing.T) {
	if New("") != nil {
		t.Fatal("New(\"\") should be nil")
	}
	// nil client Search is a no-op, not a panic.
	id, err := New("").Search(context.Background(), "a", "b")
	if id != "" || err != nil {
		t.Fatalf("nil Search = (%q,%v), want (\"\",nil)", id, err)
	}
}

func TestSearchParsesTopEmbeddableVideo(t *testing.T) {
	var gotQuery, gotEmbeddable string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("q")
		gotEmbeddable = r.URL.Query().Get("videoEmbeddable")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"id":{"videoId":"dQw4w9WgXcQ"}}]}`))
	}))
	defer srv.Close()

	c := &Client{http: srv.Client(), key: "k", apiURL: srv.URL}
	id, err := c.Search(context.Background(), "Rick Astley", "Never Gonna Give You Up")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if id != "dQw4w9WgXcQ" {
		t.Fatalf("videoId = %q", id)
	}
	if gotQuery != "Rick Astley Never Gonna Give You Up" {
		t.Fatalf("query = %q", gotQuery)
	}
	if gotEmbeddable != "true" {
		t.Fatalf("videoEmbeddable = %q, want true", gotEmbeddable)
	}
}

func TestSearchRejectsMalformedID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"items":[{"id":{"videoId":"not-a-valid-id-too-long"}}]}`))
	}))
	defer srv.Close()
	c := &Client{http: srv.Client(), key: "k", apiURL: srv.URL}
	id, err := c.Search(context.Background(), "x", "y")
	if err != nil || id != "" {
		t.Fatalf("malformed id should yield (\"\",nil), got (%q,%v)", id, err)
	}
}

func TestSearchHTTPErrorIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden) // quota exceeded
	}))
	defer srv.Close()
	c := &Client{http: srv.Client(), key: "k", apiURL: srv.URL}
	if _, err := c.Search(context.Background(), "x", "y"); err == nil {
		t.Fatal("403 should return an error so the caller link-outs")
	}
}
