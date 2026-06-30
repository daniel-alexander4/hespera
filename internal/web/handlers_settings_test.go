package web

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestOpenSubtitlesCombinedForm covers the merged key+UA form: both save
// together; a blank key on a later submit keeps the stored key (so editing the
// UA can't wipe it); a blank UA reverts to the default.
func TestOpenSubtitlesCombinedForm(t *testing.T) {
	h, _ := newTestHandler(t)
	router := h.Router()
	ctx := context.Background()
	post := func(key, ua string) {
		t.Helper()
		body := strings.NewReader(url.Values{"opensubtitles_api_key": {key}, "opensubtitles_user_agent": {ua}}.Encode())
		req := httptest.NewRequest(http.MethodPost, "/settings/api-keys", body)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("expected 303, got %d", rec.Code)
		}
	}

	post("mykey123", "MyApp v2")
	if got := h.effectiveOpenSubtitlesKey(ctx); got != "mykey123" {
		t.Fatalf("key = %q, want mykey123", got)
	}
	if got := h.effectiveOpenSubtitlesUserAgent(ctx); got != "MyApp v2" {
		t.Fatalf("UA = %q, want MyApp v2", got)
	}

	// Blank key + new UA → key kept, UA updated (no wipe).
	post("", "MyApp v3")
	if got := h.effectiveOpenSubtitlesKey(ctx); got != "mykey123" {
		t.Fatalf("blank submit wiped the key: %q", got)
	}
	if got := h.effectiveOpenSubtitlesUserAgent(ctx); got != "MyApp v3" {
		t.Fatalf("UA = %q, want MyApp v3", got)
	}

	// Blank UA → reverts to the built-in default.
	post("", "")
	if got := h.effectiveOpenSubtitlesUserAgent(ctx); got != "Hespera v1.0" {
		t.Fatalf("blank UA = %q, want default Hespera v1.0", got)
	}
}

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

func TestSettingsAbout(t *testing.T) {
	h, _ := newTestHandler(t)
	router := h.Router()

	t.Run("GET /settings/about 200", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/settings/about", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
	})

	t.Run("POST /settings/about 405", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/settings/about", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("expected 405, got %d", rec.Code)
		}
	})
}

// TestAboutPageTMDBNotice guards the verbatim TMDB attribution (required by
// TMDB's API terms) against accidental removal. Asserted against the real
// template — the test stub renders placeholder content — read from the
// package-relative path.
func TestAboutPageTMDBNotice(t *testing.T) {
	const notice = "This product uses the TMDB API but is not endorsed or certified by TMDB."
	b, err := os.ReadFile(filepath.Join("..", "..", "web", "templates", "settings_about.html"))
	if err != nil {
		t.Fatalf("read settings_about.html: %v", err)
	}
	if !strings.Contains(string(b), notice) {
		t.Fatalf("settings_about.html must contain the verbatim TMDB notice: %q", notice)
	}
}

func TestSettingsJobsFragment(t *testing.T) {
	h, db := newTestHandler(t)
	router := h.Router()

	res, err := db.Exec("INSERT INTO libraries (name, type, root_path) VALUES ('TV','tv','/media/tv')")
	if err != nil {
		t.Fatalf("seed library: %v", err)
	}
	libID, _ := res.LastInsertId()
	if _, err := db.Exec(
		"INSERT INTO scan_jobs (library_id, job_type, status, progress_current, progress_total, created_at) VALUES (?, 'tvscan', 'done', 76, 76, datetime('now'))",
		libID); err != nil {
		t.Fatalf("seed job: %v", err)
	}

	t.Run("GET fragment renders just the job table", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/settings/jobs/fragment", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, "tvscan") {
			t.Fatalf("fragment missing the tvscan row: %s", body)
		}
		if !strings.Contains(body, "badge-done") {
			t.Fatalf("fragment missing the status badge")
		}
		// It is a fragment, not the full page — no layout chrome.
		if strings.Contains(body, "<html") || strings.Contains(body, "page-header") {
			t.Fatalf("fragment should be only the table, got full page")
		}
	})

	t.Run("POST fragment 405", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/settings/jobs/fragment", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status = %d, want 405", rec.Code)
		}
	})

	t.Run("jobs page renders the row and the fragment template", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/settings/jobs", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "tvscan") {
			t.Fatalf("jobs page missing the tvscan row")
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
