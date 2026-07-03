package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestIntegrityReportPage pins the drill-down report: flagged files in the
// corrupt section with a replace-the-file mitigation, degraded files in the
// playable section with the silence-fill explanation, resolved titles, and
// the libraries-page pills linking here.
func TestIntegrityReportPage(t *testing.T) {
	h, db := newTestHandler(t)

	if _, err := db.Exec(`INSERT INTO libraries (id, name, type, root_path) VALUES (7, 'TV', 'tv', ?)`, h.cfg.MediaRoot); err != nil {
		t.Fatalf("seed library: %v", err)
	}
	seed := func(path, status, detail string) int64 {
		t.Helper()
		res, err := db.Exec(`INSERT INTO tv_series_files (library_id, abs_path, integrity_status, integrity_detail, integrity_checked_at, file_size_bytes) VALUES (7, ?, ?, ?, '2026-07-02 19:56:52', 1234567)`,
			path, status, detail)
		if err != nil {
			t.Fatalf("seed file: %v", err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	badID := seed("/m/tv/Show/s1/e1.mkv", "flagged", "bitstream corruption (4 decode errors) — data loss, not auto-repairable")
	gapID := seed("/m/tv/Show/s2/e2.mkv", "degraded", "container remuxed (6 errors dropped); audio gap 3.9s (missing audio) — playable: the transcoder silence-fills the gap; replace the file to restore the missing audio")
	for id, season := range map[int64]int{badID: 1, gapID: 2} {
		if _, err := db.Exec(`INSERT INTO tv_series_identities (file_id, status, provider, series_id, season_number, episode_numbers_csv, guessed_title) VALUES (?, 'matched', 'tmdb', '42', ?, '1', 'Doctor Who')`, id, season); err != nil {
			t.Fatalf("seed identity: %v", err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/libraries/integrity-report?id=7", nil)
	rec := httptest.NewRecorder()
	h.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("report: %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"Corrupt — needs replacement (1)",
		"Degraded — playable with known defects (1)",
		"bitstream corruption (4 decode errors)",
		"audio gap 3.9s",
		"Replace the file from another source", // flagged mitigation
		"fills the missing audio with silence", // degraded mitigation
		"Doctor Who — S01E01",                   // resolved title (guessed_title fallback)
		"/tv/season/?series=42&amp;season=2",   // owning-page link
		"/m/tv/Show/s1/e1.mkv",                 // path shown
		"1.2 MiB",                              // humanBytes
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("report missing %q", want)
		}
	}

	// Unknown library → 404; bad id → 400.
	rec = httptest.NewRecorder()
	h.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/libraries/integrity-report?id=999", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown library: %d, want 404", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/libraries/integrity-report?id=abc", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad id: %d, want 400", rec.Code)
	}

	// Libraries page: the corrupt pill links to the report and counts only
	// flagged; the degraded link appears alongside.
	rec = httptest.NewRecorder()
	h.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/libraries", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("libraries: %d", rec.Code)
	}
	lb := rec.Body.String()
	if !strings.Contains(lb, `href="/libraries/integrity-report?id=7"`) {
		t.Fatal("corrupt pill is not a report link")
	}
	if !strings.Contains(lb, "1 corrupt") {
		t.Fatal("flagged count wrong (degraded must not count as corrupt)")
	}
	if !strings.Contains(lb, "1 degraded") {
		t.Fatal("degraded link missing")
	}
}
