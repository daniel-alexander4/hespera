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

func TestFirstEmbeddableSkipsUnplayable(t *testing.T) {
	var gotIDs string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotIDs = r.URL.Query().Get("id")
		w.Header().Set("Content-Type", "application/json")
		// AAA not embeddable, BBB US-blocked, CCC embeddable+clean, DDD embeddable.
		_, _ = w.Write([]byte(`{"items":[
			{"id":"aaaaaaaaaaa","status":{"embeddable":false,"uploadStatus":"processed"}},
			{"id":"bbbbbbbbbbb","status":{"embeddable":true,"uploadStatus":"processed"},"contentDetails":{"regionRestriction":{"blocked":["US"]}}},
			{"id":"ccccccccccc","status":{"embeddable":true,"uploadStatus":"processed"}},
			{"id":"ddddddddddd","status":{"embeddable":true,"uploadStatus":"processed"}}
		]}`))
	}))
	defer srv.Close()
	c := &Client{http: srv.Client(), key: "k", apiURL: srv.URL}

	id, err := c.FirstEmbeddable(context.Background(), []string{"aaaaaaaaaaa", "bbbbbbbbbbb", "ccccccccccc", "ddddddddddd"})
	if err != nil {
		t.Fatalf("FirstEmbeddable: %v", err)
	}
	if id != "ccccccccccc" {
		t.Fatalf("id = %q, want the first embeddable+unblocked candidate ccccccccccc", id)
	}
	if gotIDs != "aaaaaaaaaaa,bbbbbbbbbbb,ccccccccccc,ddddddddddd" {
		t.Fatalf("batched id param = %q", gotIDs)
	}
}

func TestFirstEmbeddableNoneQualify(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"items":[{"id":"aaaaaaaaaaa","status":{"embeddable":false}}]}`))
	}))
	defer srv.Close()
	c := &Client{http: srv.Client(), key: "k", apiURL: srv.URL}
	id, err := c.FirstEmbeddable(context.Background(), []string{"aaaaaaaaaaa"})
	if err != nil || id != "" {
		t.Fatalf("none-embeddable = (%q,%v), want (\"\",nil)", id, err)
	}
}

func TestFirstEmbeddableNilAndEmpty(t *testing.T) {
	if id, err := New("").FirstEmbeddable(context.Background(), []string{"aaaaaaaaaaa"}); id != "" || err != nil {
		t.Fatalf("nil client = (%q,%v), want (\"\",nil)", id, err)
	}
	c := &Client{http: http.DefaultClient, key: "k", apiURL: "http://unused"}
	if id, err := c.FirstEmbeddable(context.Background(), nil); id != "" || err != nil {
		t.Fatalf("empty ids = (%q,%v), want (\"\",nil)", id, err)
	}
}

func TestFirstEmbeddableHTTPErrorIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	c := &Client{http: srv.Client(), key: "k", apiURL: srv.URL}
	if _, err := c.FirstEmbeddable(context.Background(), []string{"aaaaaaaaaaa"}); err == nil {
		t.Fatal("a videos.list HTTP error must surface so the caller reports unavailable")
	}
}
