package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
	if !strings.Contains(out["embedUrl"], "youtube-nocookie.com/embed/dQw4w9WgXcQ") {
		t.Fatalf("embedUrl wrong: %v", out)
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
