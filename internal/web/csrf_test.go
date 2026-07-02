package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// okHandler is the next handler csrfGuard wraps; it records whether it ran.
func okHandler(ran *bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*ran = true
		w.WriteHeader(http.StatusOK)
	})
}

func TestCSRFGuard(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		host       string
		origin     string
		referer    string
		wantPassed bool // true = reached next handler; false = 403
	}{
		{name: "safe GET always passes", method: http.MethodGet, host: "127.0.0.1:8080", origin: "https://evil.example", wantPassed: true},
		{name: "safe HEAD always passes", method: http.MethodHead, host: "127.0.0.1:8080", origin: "https://evil.example", wantPassed: true},
		{name: "POST same-origin passes", method: http.MethodPost, host: "127.0.0.1:8080", origin: "http://127.0.0.1:8080", wantPassed: true},
		{name: "POST no origin/referer passes", method: http.MethodPost, host: "127.0.0.1:8080", wantPassed: true},
		{name: "POST cross-origin denied", method: http.MethodPost, host: "127.0.0.1:8080", origin: "https://evil.example", wantPassed: false},
		{name: "POST cross-origin via referer denied", method: http.MethodPost, host: "127.0.0.1:8080", referer: "https://evil.example/attack", wantPassed: false},
		{name: "POST mismatched port denied", method: http.MethodPost, host: "127.0.0.1:8080", origin: "http://127.0.0.1:9999", wantPassed: false},
		{name: "PUT cross-origin denied", method: http.MethodPut, host: "127.0.0.1:8080", origin: "https://evil.example", wantPassed: false},
		{name: "DELETE cross-origin denied", method: http.MethodDelete, host: "127.0.0.1:8080", origin: "https://evil.example", wantPassed: false},
		{name: "POST same host referer passes", method: http.MethodPost, host: "127.0.0.1:8080", referer: "http://127.0.0.1:8080/libraries", wantPassed: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ran := false
			h := csrfGuard(okHandler(&ran))

			req := httptest.NewRequest(tt.method, "/libraries/delete", nil)
			req.Host = tt.host
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			if tt.referer != "" {
				req.Header.Set("Referer", tt.referer)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if tt.wantPassed {
				if !ran {
					t.Fatalf("expected request to reach next handler, got status %d", rec.Code)
				}
			} else {
				if ran {
					t.Fatal("cross-origin unsafe request should not reach next handler")
				}
				if rec.Code != http.StatusForbidden {
					t.Fatalf("expected 403, got %d", rec.Code)
				}
			}
		})
	}
}

// TestCSRFGuardJSONResponse verifies a denied JSON request gets a JSON 403.
func TestCSRFGuardJSONResponse(t *testing.T) {
	ran := false
	h := csrfGuard(okHandler(&ran))

	req := httptest.NewRequest(http.MethodPost, "/libraries/delete", nil)
	req.Host = "127.0.0.1:8080"
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if ran {
		t.Fatal("cross-origin request should not reach next handler")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("expected JSON content-type, got %q", ct)
	}
}
