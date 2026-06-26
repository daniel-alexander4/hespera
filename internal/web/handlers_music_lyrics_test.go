package web

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func TestPickBestLrcLibCandidate(t *testing.T) {
	cands := []lrcLibCandidate{
		{TrackName: "Wrong Song", ArtistName: "Other", SyncedLyrics: "[00:01.00] x"},
		{TrackName: "My Song", ArtistName: "The Band", AlbumName: "The Album", SyncedLyrics: "[00:01.00] right", PlainLyrics: "right"},
		{TrackName: "My Song (Live)", ArtistName: "The Band", PlainLyrics: "partial"},
		{TrackName: "My Song", ArtistName: "The Band", PlainLyrics: ""}, // no lyrics -> skipped
	}
	best := pickBestLrcLibCandidate(cands, "My Song", "The Band", "The Album")
	if best == nil || best.AlbumName != "The Album" {
		t.Fatalf("expected the exact track+artist+album match, got %+v", best)
	}

	if pickBestLrcLibCandidate([]lrcLibCandidate{{TrackName: "x", PlainLyrics: ""}}, "a", "b", "") != nil {
		t.Fatal("candidate with no lyrics must not be selected")
	}
	if pickBestLrcLibCandidate(nil, "a", "b", "") != nil {
		t.Fatal("no candidates -> nil")
	}
}

func TestNormalizeLrcText(t *testing.T) {
	cases := map[string]string{
		"  Hello,  World!  ": "hello world",
		"Café Münch":         "caf m nch",
		"AC/DC":              "ac dc",
		"":                   "",
	}
	for in, want := range cases {
		if got := normalizeLrcText(in); got != want {
			t.Errorf("normalizeLrcText(%q) = %q, want %q", in, got, want)
		}
	}
}

func insertMusicTrack(t *testing.T, db *sql.DB, title, artist, album string) int64 {
	t.Helper()
	lib, _ := db.Exec("INSERT INTO libraries(name,type,root_path) VALUES('M','music','/m')")
	libID, _ := lib.LastInsertId()
	ar, err := db.Exec("INSERT INTO music_artists(library_id,name) VALUES(?,?)", libID, artist)
	if err != nil {
		t.Fatalf("insert artist: %v", err)
	}
	arID, _ := ar.LastInsertId()
	al, err := db.Exec("INSERT INTO music_albums(library_id,artist_id,title,year) VALUES(?,?,?,2020)", libID, arID, album)
	if err != nil {
		t.Fatalf("insert album: %v", err)
	}
	alID, _ := al.LastInsertId()
	tr, err := db.Exec(
		"INSERT INTO music_tracks(library_id,artist_id,album_id,title,track_no,disc_no,abs_path,mime_type) VALUES(?,?,?,?,1,1,?,?)",
		libID, arID, alID, title, "/m/"+title+".mp3", "audio/mpeg",
	)
	if err != nil {
		t.Fatalf("insert track: %v", err)
	}
	id, _ := tr.LastInsertId()
	return id
}

// fakeLRCLIB serves /api/get and /api/search; getBody="" => 404 on get.
func fakeLRCLIB(t *testing.T, hits *int64, getBody string, searchBody string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(hits, 1)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/get"):
			if getBody == "" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write([]byte(getBody))
		case strings.HasPrefix(r.URL.Path, "/api/search"):
			_, _ = w.Write([]byte(searchBody))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func postLyrics(t *testing.T, h *Handler, trackID int64) (int, map[string]any) {
	t.Helper()
	body := "track_id=" + url.QueryEscape(strconv.FormatInt(trackID, 10))
	req := httptest.NewRequest(http.MethodPost, "/music/lyrics/fetch", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	h.musicLyricsFetch(rr, req)
	var out struct {
		OK   bool           `json:"ok"`
		Data map[string]any `json:"data"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	return rr.Code, out.Data
}

func TestMusicLyricsFetchSyncedHitThenCache(t *testing.T) {
	h, db := newTestHandler(t)
	id := insertMusicTrack(t, db, "My Song", "The Band", "The Album")

	var hits int64
	getBody := `{"id":42,"trackName":"My Song","artistName":"The Band","albumName":"The Album","syncedLyrics":"[00:01.00] hello","plainLyrics":"hello"}`
	srv := fakeLRCLIB(t, &hits, getBody, "[]")
	orig := lrcLibBaseURL
	lrcLibBaseURL = srv.URL
	defer func() { lrcLibBaseURL = orig }()

	code, data := postLyrics(t, h, id)
	if code != http.StatusOK || data["synced"] != true {
		t.Fatalf("first fetch: code=%d data=%v", code, data)
	}
	if !strings.Contains(data["synced_lyrics"].(string), "[00:01.00] hello") {
		t.Fatalf("synced_lyrics not returned: %v", data["synced_lyrics"])
	}
	if data["cached"] != false {
		t.Fatalf("first fetch should not be cached")
	}
	firstHits := atomic.LoadInt64(&hits)
	if firstHits == 0 {
		t.Fatal("expected at least one provider request")
	}

	// Second call must serve from cache — no further provider requests.
	code, data = postLyrics(t, h, id)
	if code != http.StatusOK || data["cached"] != true || data["synced"] != true {
		t.Fatalf("second fetch should be a cache hit: code=%d data=%v", code, data)
	}
	if atomic.LoadInt64(&hits) != firstHits {
		t.Fatalf("cache hit should not re-query provider (hits %d -> %d)", firstHits, atomic.LoadInt64(&hits))
	}
}

func TestMusicLyricsFetchMissIsCached(t *testing.T) {
	h, db := newTestHandler(t)
	id := insertMusicTrack(t, db, "Obscure", "Nobody", "")

	var hits int64
	srv := fakeLRCLIB(t, &hits, "", "[]") // get 404, search empty
	orig := lrcLibBaseURL
	lrcLibBaseURL = srv.URL
	defer func() { lrcLibBaseURL = orig }()

	code, data := postLyrics(t, h, id)
	if code != http.StatusOK || data["synced"] != false || data["synced_lyrics"] != "" {
		t.Fatalf("miss: code=%d data=%v", code, data)
	}
	missHits := atomic.LoadInt64(&hits)

	// A cached miss must not re-query the provider.
	code, data = postLyrics(t, h, id)
	if code != http.StatusOK || data["cached"] != true {
		t.Fatalf("second fetch should be cached miss: code=%d data=%v", code, data)
	}
	if atomic.LoadInt64(&hits) != missHits {
		t.Fatalf("cached miss should not re-query provider (hits %d -> %d)", missHits, atomic.LoadInt64(&hits))
	}
}

func TestMusicLyricsFetchBadRequest(t *testing.T) {
	h, _ := newTestHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/music/lyrics/fetch", strings.NewReader("track_id=0"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	h.musicLyricsFetch(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("track_id=0 should be 400, got %d", rr.Code)
	}
}
