package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSettingsHandlers(t *testing.T) {
	h, _ := newTestHandler(t)
	router := h.Router()

	t.Run("GET /settings 200", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/settings", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
	})

	t.Run("POST /settings 405", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/settings", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("expected 405, got %d", rec.Code)
		}
	})
}

func TestLibraryHandlers(t *testing.T) {
	h, db := newTestHandler(t)
	router := h.Router()

	t.Run("GET /libraries 200", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/libraries", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
	})

	t.Run("POST /libraries 405", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/libraries", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("expected 405, got %d", rec.Code)
		}
	})

	t.Run("GET /libraries/new 200", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/libraries/new", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
	})

	t.Run("POST /libraries/new creates library", func(t *testing.T) {
		mediaRoot := h.cfg.MediaRoot
		musicDir := filepath.Join(mediaRoot, "music")
		if err := os.MkdirAll(musicDir, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}

		body := "name=Test+Music&type=music&root_path=" + musicDir
		req := httptest.NewRequest(http.MethodPost, "/libraries/new",
			strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("expected 303, got %d; body: %s", rec.Code, rec.Body.String())
		}
		loc := rec.Header().Get("Location")
		if loc != "/libraries" {
			t.Fatalf("expected redirect to /libraries, got %s", loc)
		}

		var name string
		if err := db.QueryRow("SELECT name FROM libraries WHERE name='Test Music'").Scan(&name); err != nil {
			t.Fatalf("library row not found: %v", err)
		}
	})

	t.Run("POST /libraries/new empty name 400", func(t *testing.T) {
		body := "name=&type=music&root_path=/whatever"
		req := httptest.NewRequest(http.MethodPost, "/libraries/new",
			strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d", rec.Code)
		}
	})

	t.Run("POST /libraries/new invalid type 400", func(t *testing.T) {
		body := "name=Bad&type=invalid&root_path=/whatever"
		req := httptest.NewRequest(http.MethodPost, "/libraries/new",
			strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d", rec.Code)
		}
	})

	t.Run("POST /libraries/new bad root 400", func(t *testing.T) {
		body := "name=Bad&type=music&root_path=/outside/media"
		req := httptest.NewRequest(http.MethodPost, "/libraries/new",
			strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d", rec.Code)
		}
	})

	t.Run("POST /libraries/scan enqueues job", func(t *testing.T) {
		res, err := db.Exec("INSERT INTO libraries (name, type, root_path) VALUES ('Scan Lib', 'music', ?)",
			filepath.Join(h.cfg.MediaRoot, "scanlib"))
		if err != nil {
			t.Fatalf("insert library: %v", err)
		}
		libID, _ := res.LastInsertId()

		body := fmt.Sprintf("id=%d", libID)
		req := httptest.NewRequest(http.MethodPost, "/libraries/scan",
			strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("expected 303, got %d; body: %s", rec.Code, rec.Body.String())
		}

		var jobCount int
		if err := db.QueryRow("SELECT COUNT(*) FROM scan_jobs WHERE library_id=?", libID).Scan(&jobCount); err != nil {
			t.Fatalf("query scan_jobs: %v", err)
		}
		if jobCount == 0 {
			t.Fatal("expected at least one scan_jobs row")
		}
	})

	t.Run("POST /libraries/scan not found", func(t *testing.T) {
		body := "id=99999"
		req := httptest.NewRequest(http.MethodPost, "/libraries/scan",
			strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("expected 404, got %d", rec.Code)
		}
	})

	t.Run("POST /libraries/delete", func(t *testing.T) {
		res, err := db.Exec("INSERT INTO libraries (name, type, root_path) VALUES ('Delete Me', 'music', ?)",
			filepath.Join(h.cfg.MediaRoot, "deleteme"))
		if err != nil {
			t.Fatalf("insert library: %v", err)
		}
		libID, _ := res.LastInsertId()

		body := fmt.Sprintf("id=%d", libID)
		req := httptest.NewRequest(http.MethodPost, "/libraries/delete",
			strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("expected 303, got %d; body: %s", rec.Code, rec.Body.String())
		}

		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM libraries WHERE id=?", libID).Scan(&count); err != nil {
			t.Fatalf("query libraries: %v", err)
		}
		if count != 0 {
			t.Fatal("expected library to be deleted")
		}
	})
}
