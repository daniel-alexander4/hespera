package opensubtitles

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewNilOnEmptyKey(t *testing.T) {
	if New("", "Some App v1") != nil {
		t.Fatal("New(\"\") should return nil so callers can gate on a nil client")
	}
	if New("abc", "Some App v1") == nil {
		t.Fatal("New with a key should return a client")
	}
	// An empty UA falls back to the registered default, never a blank UA.
	if c := New("abc", ""); c == nil || c.userAgent != defaultUserAgent {
		t.Fatalf("empty UA should default to %q, got %+v", defaultUserAgent, c)
	}
}

func TestSearchParsesResultsAndSkipsFileless(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.String()
		if r.Header.Get("Api-Key") != "k" || r.Header.Get("User-Agent") != "TestApp v1" {
			t.Errorf("missing/incorrect auth headers: %v / %v", r.Header.Get("Api-Key"), r.Header.Get("User-Agent"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"attributes": map[string]any{
					"language": "en", "download_count": 42, "hearing_impaired": false, "release": "GROUP.x264",
					"files": []map[string]any{{"file_id": 999, "file_name": "ep.srt"}},
				}},
				{"attributes": map[string]any{ // no files → skipped
					"language": "en", "release": "no-files", "files": []map[string]any{},
				}},
			},
		})
	}))
	defer srv.Close()

	c := New("k", "TestApp v1")
	c.baseURL = srv.URL

	res, err := c.Search(context.Background(), "1396", 2, 5, "en")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("want 1 result (fileless one skipped), got %d", len(res))
	}
	if res[0].FileID != 999 || res[0].DownloadCount != 42 || res[0].Release != "GROUP.x264" {
		t.Fatalf("unexpected result: %+v", res[0])
	}
	for _, want := range []string{"parent_tmdb_id=1396", "season_number=2", "episode_number=5", "languages=en"} {
		if !strings.Contains(gotPath, want) {
			t.Errorf("query missing %q; got %s", want, gotPath)
		}
	}
}

func TestSearchRejectsBadArgs(t *testing.T) {
	c := New("k", "")
	if _, err := c.Search(context.Background(), "", 1, 1, "en"); err == nil {
		t.Error("empty series id should error")
	}
	if _, err := c.Search(context.Background(), "1396", 1, 0, "en"); err == nil {
		t.Error("episode 0 should error")
	}
}

func TestDownloadReturnsLink(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("want POST, got %s", r.Method)
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "\"file_id\":999") {
			t.Errorf("body missing file_id: %s", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"link": "https://www.opensubtitles.com/download/abc/sub.srt", "remaining": 99})
	}))
	defer srv.Close()

	c := New("k", "")
	c.baseURL = srv.URL

	link, err := c.Download(context.Background(), 999)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if link != "https://www.opensubtitles.com/download/abc/sub.srt" {
		t.Fatalf("unexpected link: %s", link)
	}
}

func TestNilClientIsNoOp(t *testing.T) {
	var c *OSClient
	if res, err := c.Search(context.Background(), "1", 1, 1, "en"); res != nil || err != nil {
		t.Errorf("nil Search should be a no-op, got %v / %v", res, err)
	}
	if _, err := c.Download(context.Background(), 1); err == nil {
		t.Error("nil Download should error (no key configured)")
	}
}
