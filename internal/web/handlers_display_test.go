package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestDisplayScaleEndpoint pins the auto-scale contract: app mode answers with
// the stubbed class for the window's point; server mode always answers unknown
// (a remote browser must never inherit the server's display class).
func TestDisplayScaleEndpoint(t *testing.T) {
	h, _ := newTestHandler(t)
	h.displayClassAt = func(_ context.Context, x, y int) string {
		if x == 2000 {
			return "tv"
		}
		return "desktop"
	}

	get := func(url string) map[string]string {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, url, nil)
		rec := httptest.NewRecorder()
		h.Router().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: %d", url, rec.Code)
		}
		var out map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("bad JSON: %v", err)
		}
		return out
	}

	// Server mode (the test handler default): unknown regardless of the stub.
	if got := get("/display/scale?x=2000&y=10")["class"]; got != "" {
		t.Fatalf("server mode class = %q, want unknown", got)
	}

	// App mode: the stubbed lookup answers per-point.
	h.appMode = true
	if got := get("/display/scale?x=2000&y=10")["class"]; got != "tv" {
		t.Fatalf("app mode class = %q, want tv", got)
	}
	if got := get("/display/scale?x=5&y=5")["class"]; got != "desktop" {
		t.Fatalf("app mode class = %q, want desktop", got)
	}

	// Method guard.
	req := httptest.NewRequest(http.MethodPost, "/display/scale", nil)
	rec := httptest.NewRecorder()
	h.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST = %d, want 405", rec.Code)
	}
}
