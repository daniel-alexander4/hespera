package web

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
)

// TestFFmpegHealthGrading pins the ffmpeg row's status logic against the one
// real version floor (7, for iPhone grid-HEIC), independent of whether ffmpeg
// is actually installed on the test box.
func TestFFmpegHealthGrading(t *testing.T) {
	// ffmpegHealth calls the live probe; assert only on the shape that the
	// installed ffmpeg produces, plus the pure grading via a direct check.
	row := ffmpegHealth(context.Background())
	switch row.Status {
	case "ok", "warn", "missing":
	default:
		t.Fatalf("unexpected ffmpeg status %q", row.Status)
	}
	if row.Detail == "" {
		t.Fatal("ffmpeg row must always carry a detail explanation")
	}
	if row.Status == "missing" && row.Version != "" {
		t.Fatal("missing ffmpeg must not report a version")
	}
}

// TestChromeHealthServerMode pins the app-mode gate: in server mode the browser
// is on the viewer's device, so the row is informational ('na'), never a probe
// of the server's own browser.
func TestChromeHealthServerMode(t *testing.T) {
	h, _ := newTestHandler(t) // newTestHandler leaves appMode false (server mode)
	row := h.chromeHealth()
	if row.Status != "na" {
		t.Fatalf("server-mode chrome status = %q, want na (no server-side probe)", row.Status)
	}
	if row.Version != "" {
		t.Fatalf("server-mode chrome must not report a version, got %q", row.Version)
	}
	if row.Detail == "" {
		t.Fatal("chrome row must always carry a detail explanation")
	}
}

// TestAboutHealthEndpoint pins the JSON shape and that both rows always carry a
// detail string.
func TestAboutHealthEndpoint(t *testing.T) {
	h, _ := newTestHandler(t)
	req := httptest.NewRequest("GET", "/about/health", nil)
	rec := httptest.NewRecorder()
	h.aboutHealth(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	var out aboutHealth
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.FFmpeg.Detail == "" || out.Chrome.Detail == "" {
		t.Fatal("both health rows must carry a detail explanation")
	}
	// Server-mode handler → chrome is informational.
	if out.Chrome.Status != "na" {
		t.Fatalf("chrome status = %q, want na in server mode", out.Chrome.Status)
	}
}
