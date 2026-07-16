package web

// Tests for the external_metadata_enabled umbrella off-switch: with the toggle
// off Hespera makes no outbound metadata calls — match jobs aren't chained or
// accepted, the lazy-fetch funnels enqueue nothing, and the subtitle / lyrics /
// update-check handlers refuse before any network — while cached data still
// serves and the local job tail keeps running.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hespera/internal/match"
	"hespera/internal/tmdb"
)

// setExternalMetadata stores the toggle the way the settings form does:
// default-ON is absence of the row; off is an explicit '0'.
func setExternalMetadata(t *testing.T, h *Handler, on bool) {
	t.Helper()
	var err error
	if on {
		_, err = h.db.Exec("DELETE FROM app_settings WHERE key='external_metadata_enabled'")
	} else {
		_, err = h.db.Exec("INSERT INTO app_settings (key, value) VALUES ('external_metadata_enabled', '0') ON CONFLICT(key) DO UPDATE SET value='0'")
	}
	if err != nil {
		t.Fatalf("set external_metadata_enabled: %v", err)
	}
}

func scanJobCount(t *testing.T, h *Handler, jobType string) int {
	t.Helper()
	var n int
	if err := h.db.QueryRow("SELECT COUNT(*) FROM scan_jobs WHERE job_type=?", jobType).Scan(&n); err != nil {
		t.Fatalf("count %s jobs: %v", jobType, err)
	}
	return n
}

func TestEffectiveExternalMetadataEnabledDefaultsOn(t *testing.T) {
	h, _ := newTestHandler(t)
	ctx := context.Background()
	if !h.effectiveExternalMetadataEnabled(ctx) {
		t.Fatal("default (no row) should be ON")
	}
	setExternalMetadata(t, h, false)
	if h.effectiveExternalMetadataEnabled(ctx) {
		t.Fatal("stored '0' should read as OFF")
	}
	setExternalMetadata(t, h, true)
	if !h.effectiveExternalMetadataEnabled(ctx) {
		t.Fatal("clearing the row should restore the ON default")
	}
}

func TestLazyFetchFunnelsGatedOffline(t *testing.T) {
	h, _ := newTestHandler(t)
	ctx := context.Background()
	// A TMDB key so the pre-existing key gate passes — proving the new gate,
	// not the key check, is what blocks.
	if _, err := h.db.Exec("INSERT INTO app_settings (key, value) VALUES ('tmdb_api_key', 'test-key')"); err != nil {
		t.Fatalf("seed tmdb key: %v", err)
	}

	setExternalMetadata(t, h, false)
	h.enqueueMetaFetch(ctx, "t1", "tv_cast_fetch", func(context.Context, *tmdb.Matcher) error { return nil })
	h.enqueueMovieMetaFetch(ctx, "t2", "movie_cast_fetch", func(context.Context, *tmdb.Matcher) error { return nil })
	h.enqueueMusicFetch(ctx, "t3", "artist_similar_fetch", func(context.Context, *match.Matcher) error { return nil })
	for _, jt := range []string{"tv_cast_fetch", "movie_cast_fetch", "artist_similar_fetch"} {
		if n := scanJobCount(t, h, jt); n != 0 {
			t.Fatalf("offline: %s enqueued %d job(s), want 0", jt, n)
		}
	}

	// Back on: the same call goes through and the worker runs it — the gate
	// was the only thing standing in the way.
	setExternalMetadata(t, h, true)
	ran := make(chan struct{})
	h.enqueueMusicFetch(ctx, "t4", "artist_similar_fetch", func(context.Context, *match.Matcher) error {
		close(ran)
		return nil
	})
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("online: enqueueMusicFetch job never ran")
	}
}

func TestSubtitleHandlersGatedOfflineButCacheServes(t *testing.T) {
	h, _ := newTestHandler(t)
	setExternalMetadata(t, h, false)

	for name, fn := range map[string]http.HandlerFunc{
		"tv search":    h.tvSubtitlesSearch,
		"movie search": h.movieSubtitlesSearch,
	} {
		w := httptest.NewRecorder()
		fn(w, httptest.NewRequest(http.MethodGet, "/subtitles/search?file=1&lang=en", nil))
		if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "external metadata is disabled") {
			t.Fatalf("%s offline: got %d %q, want 503 disabled", name, w.Code, w.Body.String())
		}
	}

	// Fetch of an uncached subtitle refuses…
	form := url.Values{"file_id": {"99"}, "lang": {"en"}}
	req := httptest.NewRequest(http.MethodPost, "/tv/subtitles/fetch", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.subtitlesFetch(w, req, "/tv")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("offline uncached fetch: got %d, want 503", w.Code)
	}

	// …but a cached one still serves with zero egress (the gate sits after
	// the cache check).
	dir := filepath.Join(h.cfg.DataDir, "subtitles")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "99.en.vtt"), []byte("WEBVTT\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodPost, "/tv/subtitles/fetch", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	h.subtitlesFetch(w, req, "/tv")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Fatalf("offline cached fetch: got %d %q, want 200 ok", w.Code, w.Body.String())
	}
}

func TestLyricsOfflineGateOutranksForce(t *testing.T) {
	h, _ := newTestHandler(t)
	// Lyrics feature ON so the lyrics gate passes; offline must still block.
	if _, err := h.db.Exec("INSERT INTO app_settings (key, value) VALUES ('lyrics_enabled', '1')"); err != nil {
		t.Fatal(err)
	}
	setExternalMetadata(t, h, false)

	// A real track so the handler reaches the fetch decision.
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := h.db.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	mustExec("INSERT INTO libraries (id, name, type, root_path) VALUES (1, 'm', 'music', '/tmp/x')")
	mustExec("INSERT INTO music_artists (id, library_id, name) VALUES (1, 1, 'Artist')")
	mustExec("INSERT INTO music_albums (id, library_id, artist_id, title) VALUES (1, 1, 1, 'Album')")
	mustExec("INSERT INTO music_tracks (id, library_id, artist_id, album_id, title, abs_path) VALUES (1, 1, 1, 1, 'Song', '/tmp/x/a.mp3')")

	// Any LRCLIB hit fails the test.
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true }))
	defer srv.Close()
	old := lrcLibBaseURL
	lrcLibBaseURL = srv.URL
	defer func() { lrcLibBaseURL = old }()

	form := url.Values{"track_id": {"1"}, "force": {"1"}}
	req := httptest.NewRequest(http.MethodPost, "/music/lyrics/fetch", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.musicLyricsFetch(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "external metadata disabled") {
		t.Fatalf("offline force=1: got %d %q, want benign 200 disabled", w.Code, w.Body.String())
	}
	if hit {
		t.Fatal("offline force=1 still called LRCLIB")
	}
	var n int
	if err := h.db.QueryRow("SELECT COUNT(*) FROM lyrics_cache").Scan(&n); err != nil || n != 0 {
		t.Fatalf("offline fetch cached a row (n=%d, err=%v), want none", n, err)
	}
}

func TestUpdateCheckGatedOfflineEvenOnManualClick(t *testing.T) {
	h, _ := newTestHandler(t)
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusNotFound) // "no releases yet" — a valid, network-free-to-parse answer
	}))
	defer srv.Close()
	old := githubLatestURL
	githubLatestURL = srv.URL
	defer func() { githubLatestURL = old }()

	setExternalMetadata(t, h, false)
	w := httptest.NewRecorder()
	h.updateCheck(w, httptest.NewRequest(http.MethodGet, "/update/check", nil)) // bare = the pill click
	var resp updateResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	if resp.Enabled || hits != 0 {
		t.Fatalf("offline manual check: enabled=%v hits=%d, want disabled with zero network", resp.Enabled, hits)
	}

	setExternalMetadata(t, h, true)
	w = httptest.NewRecorder()
	h.updateCheck(w, httptest.NewRequest(http.MethodGet, "/update/check", nil))
	if hits != 1 {
		t.Fatalf("online manual check: hits=%d, want 1", hits)
	}
}

func TestManualMatchHandlersRefuseOffline(t *testing.T) {
	h, _ := newTestHandler(t)
	if _, err := h.db.Exec("INSERT INTO app_settings (key, value) VALUES ('tmdb_api_key', 'test-key')"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec("INSERT INTO libraries (id, name, type, root_path) VALUES (1, 'm', 'music', '/tmp/x')"); err != nil {
		t.Fatal(err)
	}
	setExternalMetadata(t, h, false)

	post := func(fn http.HandlerFunc, target string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, target, strings.NewReader("id=1"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		fn(w, req)
		return w
	}
	for name, fn := range map[string]http.HandlerFunc{
		"musicMatch":  h.musicMatch,
		"tvMatch":     h.tvMatch,
		"moviesMatch": h.moviesMatch,
		"mgmtMatch":   h.mgmtMatch,
	} {
		w := post(fn, "/match")
		if w.Code != http.StatusBadRequest || !strings.Contains(strings.ToLower(w.Body.String()), "disabled") {
			t.Fatalf("%s offline: got %d %q, want 400 disabled", name, w.Code, w.Body.String())
		}
	}
	if n := scanJobCount(t, h, "music_match"); n != 0 {
		t.Fatalf("offline musicMatch enqueued %d job(s), want 0", n)
	}
}

func TestScanChainSkipsMatchButKeepsLocalTailOffline(t *testing.T) {
	h, _ := newTestHandler(t)
	// An empty library root INSIDE MediaRoot (pathguard) — the scan ingests 0
	// files and nothing anywhere needs the network.
	root := filepath.Join(h.cfg.MediaRoot, "musiclib")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec("INSERT INTO libraries (id, name, type, root_path) VALUES (1, 'm', 'music', ?)", root); err != nil {
		t.Fatal(err)
	}
	setExternalMetadata(t, h, false)

	if _, err := h.EnqueueLibraryScan(context.Background(), 1, "user"); err != nil {
		t.Fatalf("EnqueueLibraryScan: %v", err)
	}
	// Wait for the scan job to finish (the chain enqueues its siblings from
	// inside the scan job's body).
	deadline := time.Now().Add(10 * time.Second)
	for {
		var status string
		_ = h.db.QueryRow("SELECT status FROM scan_jobs WHERE job_type='music_scan'").Scan(&status)
		if status == "done" || status == "failed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("music_scan never finished (status=%q)", status)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if n := scanJobCount(t, h, "music_match"); n != 0 {
		t.Fatalf("offline scan chained %d music_match job(s), want 0", n)
	}
	// The local tail is untouched by the gate.
	if n := scanJobCount(t, h, "integrity_check"); n != 1 {
		t.Fatalf("offline scan chained %d integrity_check job(s), want 1", n)
	}
	if n := scanJobCount(t, h, "music_loudness"); n != 1 {
		t.Fatalf("offline scan chained %d music_loudness job(s), want 1", n)
	}
}
