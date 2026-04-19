package web

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// seedTVMetadata inserts a tv_series_metadata_cache row so tvSeriesDetail
// can find a show entry for the given seriesID.
func seedTVMetadata(t *testing.T, db *sql.DB, seriesID string) {
	t.Helper()
	entityKey := "show:" + seriesID
	payload := `{"name":"Test Show","first_air_date":"2024-01-15","status":"Returning Series","overview":"A test show","poster_path":"/test.jpg","seasons":[],"genres":[]}`
	_, err := db.Exec(
		"INSERT INTO tv_series_metadata_cache (entity_key, lang, payload_json) VALUES (?, 'en', ?)",
		entityKey, payload,
	)
	if err != nil {
		t.Fatalf("seedTVMetadata: %v", err)
	}
}

func TestTVHandlers(t *testing.T) {
	h, db := newTestHandler(t)
	router := h.Router()

	t.Run("GET /tv 200", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/tv", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
	})

	t.Run("POST /tv 405", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/tv", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("expected 405, got %d", rec.Code)
		}
	})

	t.Run("GET /tv/series/99999 404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/tv/series/99999", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("expected 404, got %d", rec.Code)
		}
		body := rec.Body.String()
		if strings.Contains(strings.ToLower(body), "sql") {
			t.Fatalf("404 body should not contain SQL error text, got: %s", body)
		}
	})

	t.Run("GET /tv/series/12345 200", func(t *testing.T) {
		seedTVMetadata(t, db, "12345")
		req := httptest.NewRequest(http.MethodGet, "/tv/series/12345", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("POST /tv/series/123 405", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/tv/series/123", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("expected 405, got %d", rec.Code)
		}
	})
}
