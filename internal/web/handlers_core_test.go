package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHomeDashboard is a smoke test: GET / renders without error both with an
// empty library and with seeded activity. (The test harness uses stub page
// templates, so this asserts the handler's data-gathering doesn't fail rather
// than the rendered markup; real template parsing is covered by
// TestNewValidTemplates.)
func TestHomeDashboard(t *testing.T) {
	t.Run("empty library", func(t *testing.T) {
		h, _ := newTestHandler(t)
		rr := httptest.NewRecorder()
		h.Router().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("GET / = %d, want 200", rr.Code)
		}
	})

	t.Run("with played music", func(t *testing.T) {
		h, db := newTestHandler(t)
		libID, artistID, albumID, trackID := seedMusicData(t, db)
		if _, err := db.Exec(
			`INSERT INTO play_history (track_id, library_id, artist_id, album_id) VALUES (?, ?, ?, ?)`,
			trackID, libID, artistID, albumID); err != nil {
			t.Fatalf("insert play_history: %v", err)
		}
		rr := httptest.NewRecorder()
		h.Router().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("GET / = %d, want 200", rr.Code)
		}
	})
}

func TestLoadRecentlyPlayedArtists(t *testing.T) {
	h, db := newTestHandler(t)
	libID, artistID, albumID, trackID := seedMusicData(t, db)

	// No plays recorded yet → empty.
	if got, err := h.loadRecentlyPlayedArtists(context.Background(), libID, 12); err != nil || len(got) != 0 {
		t.Fatalf("loadRecentlyPlayedArtists (no plays) = %v, %v; want empty, nil", got, err)
	}

	if _, err := db.Exec(
		`INSERT INTO play_history (track_id, library_id, artist_id, album_id) VALUES (?, ?, ?, ?)`,
		trackID, libID, artistID, albumID); err != nil {
		t.Fatalf("insert play_history: %v", err)
	}

	got, err := h.loadRecentlyPlayedArtists(context.Background(), libID, 12)
	if err != nil {
		t.Fatalf("loadRecentlyPlayedArtists: %v", err)
	}
	if len(got) != 1 || got[0].ID != artistID || got[0].Name != "Test Artist" {
		t.Fatalf("loadRecentlyPlayedArtists = %+v, want one [Test Artist]", got)
	}
}

func TestLoadHomeStats(t *testing.T) {
	h, db := newTestHandler(t)
	libID, _, _, _ := seedMusicData(t, db)

	s := h.loadHomeStats(context.Background(), libID)
	if s.Artists != 1 {
		t.Errorf("Artists = %d, want 1", s.Artists)
	}
	if s.Albums != 1 {
		t.Errorf("Albums = %d, want 1", s.Albums)
	}
	if s.Series != 0 || s.Episodes != 0 {
		t.Errorf("TV counts = %d/%d, want 0/0", s.Series, s.Episodes)
	}
}
