package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"hespera/internal/youtube"
)

// With no YouTube key configured, the resolver returns only a link-out searchUrl
// (no in-app embed) and never errors.
func TestYouTubeResolveNoKeyLinkOut(t *testing.T) {
	h, _ := newTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/music/youtube/resolve?artist=The+Beatles&song=Hey+Jude", nil)
	rec := httptest.NewRecorder()
	h.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var out map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := out["videoId"]; ok {
		t.Fatalf("no key should yield no videoId: %v", out)
	}
	if !strings.Contains(out["searchUrl"], "youtube.com/results") {
		t.Fatalf("searchUrl missing/wrong: %v", out)
	}
}

// A cached lookup is served straight back as an embeddable video without needing
// a key (the cache is authoritative, even across key changes).
func TestYouTubeResolveCacheHit(t *testing.T) {
	h, db := newTestHandler(t)
	key := ytLookupKey("Some Artist", "Some Song")
	if _, err := db.Exec(`INSERT INTO youtube_lookups (query_key, video_id) VALUES (?, 'dQw4w9WgXcQ')`, key); err != nil {
		t.Fatalf("seed cache: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/music/youtube/resolve?artist=Some+Artist&song=Some+Song", nil)
	rec := httptest.NewRecorder()
	h.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var out map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out["videoId"] != "dQw4w9WgXcQ" {
		t.Fatalf("cached videoId not returned: %v", out)
	}
	if !strings.Contains(out["watchUrl"], "youtube.com/watch?v=dQw4w9WgXcQ") {
		t.Fatalf("watchUrl wrong: %v", out)
	}
}

// A YouTube API ERROR (e.g. quota 403) must NOT be cached — caching it would
// permanently mark a song that IS on YouTube as "no video". The request still
// succeeds with a link-out, and the next request retries.
func TestYouTubeResolveErrorNotCached(t *testing.T) {
	h, db := newTestHandler(t)
	if _, err := db.Exec(`INSERT INTO app_settings (key, value) VALUES ('youtube_api_key', 'k')`); err != nil {
		t.Fatalf("seed key: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden) // simulate quota exceeded
	}))
	defer srv.Close()
	defer youtube.SetAPIBaseForTest(srv.URL)()

	req := httptest.NewRequest(http.MethodGet, "/music/youtube/resolve?artist=Real+Artist&song=Real+Song", nil)
	rec := httptest.NewRecorder()
	h.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var out map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if _, ok := out["videoId"]; ok {
		t.Fatalf("an errored search should yield no videoId: %v", out)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM youtube_lookups WHERE query_key=?`,
		ytLookupKey("Real Artist", "Real Song")).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("an API error must NOT be cached, but %d row(s) were written", n)
	}
}

// A SUCCESSFUL search (a real hit) is cached so the next request spends no quota.
func TestYouTubeResolveHitIsCached(t *testing.T) {
	h, db := newTestHandler(t)
	if _, err := db.Exec(`INSERT INTO app_settings (key, value) VALUES ('youtube_api_key', 'k')`); err != nil {
		t.Fatalf("seed key: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"id":{"videoId":"dQw4w9WgXcQ"}}]}`))
	}))
	defer srv.Close()
	defer youtube.SetAPIBaseForTest(srv.URL)()

	req := httptest.NewRequest(http.MethodGet, "/music/youtube/resolve?artist=A&song=B", nil)
	rec := httptest.NewRecorder()
	h.Router().ServeHTTP(rec, req)
	var out map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["videoId"] != "dQw4w9WgXcQ" {
		t.Fatalf("hit should return the videoId: %v", out)
	}
	var got string
	if err := db.QueryRow(`SELECT video_id FROM youtube_lookups WHERE query_key=?`,
		ytLookupKey("A", "B")).Scan(&got); err != nil {
		t.Fatalf("expected a cached row: %v", err)
	}
	if got != "dQw4w9WgXcQ" {
		t.Fatalf("cached video_id = %q", got)
	}
}

// A YouTube API error (quota/network) is surfaced to the client as
// "unavailable":"1" — distinct from a genuine no-match — so the player can say
// "daily limit reached" instead of mislabeling a quota wall as "no video found".
func TestYouTubeResolveUnavailableFlag(t *testing.T) {
	h, _ := newTestHandler(t)
	if _, err := h.db.Exec(`INSERT INTO app_settings (key, value) VALUES ('youtube_api_key', 'k')`); err != nil {
		t.Fatalf("seed key: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden) // quota exceeded
	}))
	defer srv.Close()
	defer youtube.SetAPIBaseForTest(srv.URL)()

	req := httptest.NewRequest(http.MethodGet, "/music/youtube/resolve?artist=Real+Artist&song=Real+Song", nil)
	rec := httptest.NewRecorder()
	h.Router().ServeHTTP(rec, req)
	var out map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["unavailable"] != "1" {
		t.Fatalf("expected unavailable flag on a quota error: %v", out)
	}
	if _, ok := out["videoId"]; ok {
		t.Fatalf("unavailable must carry no videoId: %v", out)
	}
}

func TestYouTubeResolveMissingSong(t *testing.T) {
	h, _ := newTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/music/youtube/resolve?artist=x", nil)
	rec := httptest.NewRecorder()
	h.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing song should be 400, got %d", rec.Code)
	}
}
