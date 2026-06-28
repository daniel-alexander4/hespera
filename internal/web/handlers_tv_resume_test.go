package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseStartParam(t *testing.T) {
	tests := []struct {
		raw      string
		duration float64
		want     float64
	}{
		{"", 0, 0},
		{"abc", 100, 0},
		{"-5", 100, 0},
		{"0", 100, 0},
		{"100", 200, 100},
		{"300", 200, 199}, // clamp to duration-1
		{"100", 0, 100},   // unknown duration: no upper clamp
	}
	for _, tt := range tests {
		if got := parseStartParam(tt.raw, tt.duration); got != tt.want {
			t.Errorf("parseStartParam(%q, %v) = %v, want %v", tt.raw, tt.duration, got, tt.want)
		}
	}
}

func TestLibrariesReprobeEnqueues(t *testing.T) {
	h, db := newTestHandler(t)
	router := h.Router()

	res, err := db.Exec("INSERT INTO libraries (name, type, root_path) VALUES ('TV', 'tv', ?)",
		filepath.Join(h.cfg.MediaRoot, "tv"))
	if err != nil {
		t.Fatalf("insert library: %v", err)
	}
	libID, _ := res.LastInsertId()

	req := httptest.NewRequest(http.MethodPost, "/libraries/reprobe",
		strings.NewReader(fmt.Sprintf("id=%d", libID)))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d; body: %s", rec.Code, rec.Body.String())
	}

	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM scan_jobs WHERE library_id=? AND job_type='tv_probe'", libID).Scan(&n); err != nil {
		t.Fatalf("query scan_jobs: %v", err)
	}
	if n == 0 {
		t.Fatal("expected a tv_probe scan_jobs row")
	}
}

func TestLibrariesReprobeRejectsNonTV(t *testing.T) {
	h, db := newTestHandler(t)
	router := h.Router()

	res, err := db.Exec("INSERT INTO libraries (name, type, root_path) VALUES ('Music', 'music', ?)",
		filepath.Join(h.cfg.MediaRoot, "music"))
	if err != nil {
		t.Fatalf("insert library: %v", err)
	}
	libID, _ := res.LastInsertId()

	req := httptest.NewRequest(http.MethodPost, "/libraries/reprobe",
		strings.NewReader(fmt.Sprintf("id=%d", libID)))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for non-tv library, got %d; body: %s", rec.Code, rec.Body.String())
	}
}
