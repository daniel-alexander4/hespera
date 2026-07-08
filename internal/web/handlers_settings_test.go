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
	"sync"
	"testing"
	"time"

	"hespera/internal/jobs"
)

// TestEnqueueYieldingReEnqueuesOnYield: a cosmetic job whose executor returns
// jobs.ErrYielded is re-run to finish its remaining work, and reported done
// (never failed) — the mechanism that keeps trickplay/thumb from blocking a
// waiting interactive job on the single worker.
func TestEnqueueYieldingReEnqueuesOnYield(t *testing.T) {
	h, _ := newTestHandler(t)
	runs := make(chan int, 4)
	var mu sync.Mutex
	n := 0
	h.enqueueYielding("tv_trickplay", 1, func(ctx context.Context, jobID, libID int64) error {
		mu.Lock()
		n++
		cur := n
		mu.Unlock()
		runs <- cur
		if cur == 1 {
			return jobs.ErrYielded // first run yields to (pretend) interactive work
		}
		return nil // re-enqueued run completes
	})
	for want := 1; want <= 2; want++ {
		select {
		case got := <-runs:
			if got != want {
				t.Fatalf("run order %d, want %d", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for run %d — re-enqueue on yield failed", want)
		}
	}
	select {
	case got := <-runs:
		t.Fatalf("unexpected extra run %d (yield should re-enqueue exactly once)", got)
	case <-time.After(300 * time.Millisecond):
	}
}

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
		req := httptest.NewRequest(http.MethodPost, "/settings", body)
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

	t.Run("POST /settings with no known form field redirects back", func(t *testing.T) {
		// POST is the accordion forms' dispatch; an unrecognized body just
		// bounces to the page rather than erroring.
		req := httptest.NewRequest(http.MethodPost, "/settings", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("expected 303, got %d", rec.Code)
		}
		if loc := rec.Header().Get("Location"); loc != "/settings" {
			t.Fatalf("Location = %q, want /settings", loc)
		}
	})

	t.Run("PUT /settings 405", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPut, "/settings", nil)
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

	t.Run("GET /settings/about redirects to the About card", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/settings/about", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("expected 303, got %d", rec.Code)
		}
		if loc := rec.Header().Get("Location"); loc != "/settings?open=about" {
			t.Fatalf("Location = %q, want /settings?open=about", loc)
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
	b, err := os.ReadFile(filepath.Join("..", "..", "web", "templates", "settings.html"))
	if err != nil {
		t.Fatalf("read settings.html: %v", err)
	}
	if !strings.Contains(string(b), notice) {
		t.Fatalf("settings.html (the About card) must contain the verbatim TMDB notice: %q", notice)
	}
}

// TestLastfmKeyForm covers saving + clearing the Last.fm key via its own form
// (the shared fanart/audiodb/lastfm POST loop) and effectiveLastfmKey resolution.
func TestLastfmKeyForm(t *testing.T) {
	h, _ := newTestHandler(t)
	router := h.Router()
	ctx := context.Background()
	post := func(vals url.Values) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/settings", strings.NewReader(vals.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("expected 303, got %d", rec.Code)
		}
	}
	if h.effectiveLastfmKey(ctx) != "" {
		t.Fatal("expected no Last.fm key initially")
	}
	post(url.Values{"lastfm_api_key": {"lfm-key"}})
	if got := h.effectiveLastfmKey(ctx); got != "lfm-key" {
		t.Fatalf("after save, effectiveLastfmKey = %q, want lfm-key", got)
	}
	post(url.Values{"lastfm_api_key": {""}}) // blank clears
	if got := h.effectiveLastfmKey(ctx); got != "" {
		t.Fatalf("after clear, effectiveLastfmKey = %q, want empty", got)
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
		"INSERT INTO scan_jobs (library_id, job_type, status, progress_current, progress_total, created_at) VALUES (?, 'tv_scan', 'done', 76, 76, datetime('now'))",
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
		if !strings.Contains(body, "tv_scan") {
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

	t.Run("the settings page renders the jobs row; the old URL redirects", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/settings", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "tv_scan") {
			t.Fatalf("settings page missing the tvscan jobs row")
		}
		req = httptest.NewRequest(http.MethodGet, "/settings/jobs?x=1", nil)
		rec = httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if loc := rec.Header().Get("Location"); rec.Code != http.StatusSeeOther || loc != "/settings?open=jobs&x=1" {
			t.Fatalf("old jobs URL: code=%d Location=%q, want 303 → /settings?open=jobs&x=1", rec.Code, loc)
		}
	})
}

func TestLibraryHandlers(t *testing.T) {
	h, db := newTestHandler(t)
	router := h.Router()

	t.Run("GET /libraries redirects to the Libraries card, params preserved", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/libraries?saved=mediaroot", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("expected 303, got %d", rec.Code)
		}
		if loc := rec.Header().Get("Location"); loc != "/settings?open=libraries&saved=mediaroot" {
			t.Fatalf("Location = %q, want the saved flash to survive the hop", loc)
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

	t.Run("POST /libraries/new traversal root 400", func(t *testing.T) {
		// Lexically under the media root, resolves outside it.
		body := "name=Bad&type=music&root_path=" + h.cfg.MediaRoot + "/../etc"
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

// TestLibrariesScanChainsProbe verifies that a library scan chains the reprobe
// job that heals rows whose scan-time probe failed (empty stream_info_json) —
// the automation that replaced the manual "Verify playback" button. The scan
// runs against an empty (but existing) library root, so no ffmpeg is involved;
// the chained job row is written by the worker goroutine, hence the poll.
func TestLibrariesScanChainsProbe(t *testing.T) {
	h, db := newTestHandler(t)
	router := h.Router()

	for _, tc := range []struct {
		libType, probeType string
	}{
		{"tv", "tv_probe"},
		{"movies", "movie_probe"},
	} {
		t.Run(tc.libType, func(t *testing.T) {
			root := filepath.Join(h.cfg.MediaRoot, tc.libType+"-chain")
			if err := os.MkdirAll(root, 0o755); err != nil {
				t.Fatalf("mkdir root: %v", err)
			}
			res, err := db.Exec("INSERT INTO libraries (name, type, root_path) VALUES (?, ?, ?)",
				tc.libType+" chain", tc.libType, root)
			if err != nil {
				t.Fatalf("insert library: %v", err)
			}
			libID, _ := res.LastInsertId()

			req := httptest.NewRequest(http.MethodPost, "/libraries/scan",
				strings.NewReader(fmt.Sprintf("id=%d", libID)))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != http.StatusSeeOther {
				t.Fatalf("expected 303, got %d; body: %s", rec.Code, rec.Body.String())
			}

			deadline := time.Now().Add(5 * time.Second)
			for {
				var n int
				if err := db.QueryRow("SELECT COUNT(*) FROM scan_jobs WHERE library_id=? AND job_type=?",
					libID, tc.probeType).Scan(&n); err != nil {
					t.Fatalf("query scan_jobs: %v", err)
				}
				if n > 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("no chained %s job appeared", tc.probeType)
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}
