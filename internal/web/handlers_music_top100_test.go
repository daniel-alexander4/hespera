package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hespera/internal/billboard"
	"hespera/internal/config"
)

// buildTop100Fixture turns on the chart-data feature and writes a tiny fabricated
// 1968 grid into the handler's DataDir, so the Top-100 queue resolves.
func buildTop100Fixture(t *testing.T, h *Handler) {
	t.Helper()
	if _, err := h.db.Exec("INSERT INTO app_settings (key,value) VALUES ('billboard_enabled','1') ON CONFLICT(key) DO UPDATE SET value='1'"); err != nil {
		t.Fatalf("enable billboard: %v", err)
	}
	csv := "chart_date,current_position,title,performer,previous_position,peak_position,weeks_on_chart\n" +
		"1968-09-28,2,Harper Valley P.T.A.,Jeannie C. Riley,1,1,8\n" +
		"1968-09-28,1,Hey Jude,The Beatles,3,1,3\n" +
		"1968-10-05,1,Hey Jude,The Beatles,1,1,4\n" +
		"1968-10-05,2,Fire,Arthur Brown,4,2,6\n"
	csvPath := filepath.Join(t.TempDir(), "top100.csv")
	if err := os.WriteFile(csvPath, []byte(csv), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if err := billboard.BuildIndex(h.cfg.DataDir, csvPath); err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}
}

func TestTop100Queue(t *testing.T) {
	h, _ := newTestHandler(t)
	buildTop100Fixture(t, h)
	router := h.Router()

	type qtrack struct {
		Kind   string `json:"kind"`
		Title  string `json:"title"`
		Artist string `json:"artist"`
	}
	decode := func(url string) []qtrack {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, url, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d, want 200", url, rec.Code)
		}
		var payload struct {
			Tracks []qtrack `json:"tracks"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode %s: %v (body=%q)", url, err, rec.Body.String())
		}
		return payload.Tracks
	}

	t.Run("year in order is peak-first and all yt-kind", func(t *testing.T) {
		tracks := decode("/music/queue?source=top100&y=1968")
		if len(tracks) != 3 {
			t.Fatalf("1968 queue has %d tracks, want 3 distinct songs", len(tracks))
		}
		if tracks[0].Title != "Hey Jude" {
			t.Fatalf("first track = %q, want Hey Jude (peak #1)", tracks[0].Title)
		}
		for _, tr := range tracks {
			if tr.Kind != "yt" {
				t.Fatalf("track %q kind=%q, want yt (Top 100 is YouTube-sourced)", tr.Title, tr.Kind)
			}
		}
	})

	t.Run("dir=rev reverses the order", func(t *testing.T) {
		tracks := decode("/music/queue?source=top100&y=1968&dir=rev")
		if tracks[0].Title != "Harper Valley P.T.A." {
			t.Fatalf("reversed first = %q, want Harper Valley P.T.A.", tracks[0].Title)
		}
	})

	t.Run("shuffle-all across years pools every covered year", func(t *testing.T) {
		tracks := decode("/music/queue?source=top100")
		if len(tracks) != 3 {
			t.Fatalf("all-years pool has %d tracks, want 3 (the only year)", len(tracks))
		}
	})
}

func TestMusicPlaylistsPage(t *testing.T) {
	h, db := newTestHandler(t)
	router := h.Router()
	body := func() string {
		req := httptest.NewRequest(http.MethodGet, "/music/playlists", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("playlists page = %d, want 200", rec.Code)
		}
		return rec.Body.String()
	}

	t.Run("billboard off and no key gates the Top 100 card", func(t *testing.T) {
		b := body()
		if !contains(b, `data-bbenabled="false"`) {
			t.Fatalf("expected billboard off, body=%q", b)
		}
		if !contains(b, `data-haskey="false"`) {
			t.Fatalf("expected no youtube key, body=%q", b)
		}
	})

	t.Run("billboard data + youtube key unlocks the year picker", func(t *testing.T) {
		buildTop100Fixture(t, h) // enables billboard + writes the 1968 grid
		if _, err := db.Exec("INSERT INTO app_settings (key,value) VALUES ('youtube_api_key','k') ON CONFLICT(key) DO UPDATE SET value='k'"); err != nil {
			t.Fatalf("set youtube key: %v", err)
		}
		b := body()
		if !contains(b, `data-ready="true"`) || !contains(b, `data-haskey="true"`) {
			t.Fatalf("expected ready+key, body=%q", b)
		}
		if !contains(b, `<option value="1968"`) {
			t.Fatalf("expected 1968 in the year picker, body=%q", b)
		}
		if !contains(b, `data-test="false"`) {
			t.Fatalf("Test Audio should be off without the in-app opt-in, body=%q", b)
		}
	})

	t.Run("in-app opt-in enables Test Audio", func(t *testing.T) {
		if _, err := db.Exec("INSERT INTO app_settings (key,value) VALUES ('youtube_inapp_enabled','1') ON CONFLICT(key) DO UPDATE SET value='1'"); err != nil {
			t.Fatalf("set inapp: %v", err)
		}
		if b := body(); !contains(b, `data-test="true"`) {
			t.Fatalf("expected Test Audio on, body=%q", b)
		}
	})
}

func contains(haystack, needle string) bool { return strings.Contains(haystack, needle) }

// TestEmbeddedTemplatesCompile constructs a Handler against the real embedded
// web/ assets (no stub AssetsFS), so a syntax error in any page template —
// including music_playlists.html — fails the build here rather than at runtime.
func TestEmbeddedTemplatesCompile(t *testing.T) {
	db := openTestDB(t)
	dir := t.TempDir()
	h, err := New(Deps{Cfg: config.Config{DataDir: dir, MediaRoot: dir}, DB: db})
	if err != nil {
		t.Fatalf("New with embedded assets: %v", err)
	}
	if _, ok := h.tpls["music_playlists.html"]; !ok {
		t.Fatal("music_playlists.html did not compile")
	}
}
