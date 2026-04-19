package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMatchReviewHandlers(t *testing.T) {
	h, db := newTestHandler(t)
	router := h.Router()

	t.Run("GET /music/match/review 200 with unmatched", func(t *testing.T) {
		_, _, albumID, _ := seedMusicData(t, db)

		// Mark album as unmatched so it appears on the review page.
		_, err := db.Exec(
			"UPDATE music_albums SET match_status='unmatched', match_confidence=55 WHERE id=?",
			albumID,
		)
		if err != nil {
			t.Fatalf("update album: %v", err)
		}

		req := httptest.NewRequest(http.MethodGet, "/music/match/review", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("got status %d, want %d", rec.Code, http.StatusOK)
		}

		body := rec.Body.String()
		if !strings.Contains(body, "Test Album") {
			t.Fatalf("response body should contain album title 'Test Album', got: %s", body)
		}
		if !strings.Contains(body, "Run Match") {
			t.Fatalf("response body should contain 'Run Match' button, got: %s", body)
		}
	})

	t.Run("GET /music/match/review 200 empty", func(t *testing.T) {
		// Clean up any unmatched albums from previous subtests.
		_, err := db.Exec("DELETE FROM music_albums WHERE match_status='unmatched'")
		if err != nil {
			t.Fatalf("cleanup: %v", err)
		}

		req := httptest.NewRequest(http.MethodGet, "/music/match/review", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("got status %d, want %d", rec.Code, http.StatusOK)
		}

		body := rec.Body.String()
		if !strings.Contains(body, "No albums need review") {
			t.Fatalf("response body should contain 'No albums need review', got: %s", body)
		}
	})

	t.Run("POST /music/match/review 405", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/music/match/review", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("got status %d, want %d", rec.Code, http.StatusMethodNotAllowed)
		}
	})
}
