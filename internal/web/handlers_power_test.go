package web

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// enablePowerButton turns the opt-in setting on for a test handler.
func enablePowerButton(t *testing.T, h *Handler) {
	t.Helper()
	if _, err := h.db.Exec("INSERT INTO app_settings (key, value) VALUES ('power_button_enabled','1')"); err != nil {
		t.Fatalf("enable power button: %v", err)
	}
}

// TestPowerOffLoopbackOnly pins the LAN-serving safety contract: the power
// button halts only from the machine Hespera runs on. A household device's tap
// (any non-loopback RemoteAddr) is refused and must not reach the seam.
func TestPowerOffLoopbackOnly(t *testing.T) {
	h, _ := newTestHandler(t)
	enablePowerButton(t, h)
	calls := 0
	h.powerOff = func() error { calls++; return nil }
	router := h.Router()

	post := func(remoteAddr string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/poweroff", nil)
		req.RemoteAddr = remoteAddr
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	if rec := post("192.168.1.50:41234"); rec.Code != http.StatusForbidden {
		t.Fatalf("LAN poweroff = %d, want 403", rec.Code)
	}
	if rec := post("[2001:db8::7]:41234"); rec.Code != http.StatusForbidden {
		t.Fatalf("IPv6 LAN poweroff = %d, want 403", rec.Code)
	}
	if calls != 0 {
		t.Fatalf("non-loopback request reached the power-off seam (%d calls)", calls)
	}

	if rec := post("127.0.0.1:41234"); rec.Code != http.StatusOK {
		t.Fatalf("loopback poweroff = %d, want 200", rec.Code)
	}
	if rec := post("[::1]:41234"); rec.Code != http.StatusOK {
		t.Fatalf("IPv6 loopback poweroff = %d, want 200", rec.Code)
	}
	if calls != 2 {
		t.Fatalf("power-off seam called %d times, want 2", calls)
	}
}

// TestPowerOffRequiresSetting pins the opt-in: with the setting off (the
// default) even a loopback POST is refused, so a stale page or a curl on the
// box can't halt an install whose owner never enabled the button.
func TestPowerOffRequiresSetting(t *testing.T) {
	h, _ := newTestHandler(t)
	calls := 0
	h.powerOff = func() error { calls++; return nil }

	req := httptest.NewRequest(http.MethodPost, "/poweroff", nil)
	req.RemoteAddr = "127.0.0.1:41234"
	rec := httptest.NewRecorder()
	h.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("poweroff with setting off = %d, want 403", rec.Code)
	}
	if calls != 0 {
		t.Fatalf("disabled power button reached the seam (%d calls)", calls)
	}
}

// TestPowerOffReportsFailure pins the visible-failure rule: a missing polkit
// grant makes systemctl exit non-zero, and that must surface as a 500 the UI
// can show — not a 200 that leaves the user staring at a box still running.
func TestPowerOffReportsFailure(t *testing.T) {
	h, _ := newTestHandler(t)
	enablePowerButton(t, h)
	h.powerOff = func() error { return errors.New("Access denied") }

	req := httptest.NewRequest(http.MethodPost, "/poweroff", nil)
	req.RemoteAddr = "127.0.0.1:41234"
	rec := httptest.NewRecorder()
	h.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("failed poweroff = %d, want 500", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "Access denied") {
		t.Fatalf("failure body %q does not carry the cause", body)
	}
}

// TestPowerOffMethodAndOrigin pins the rest of the destructive-endpoint guard:
// GET is refused, and a cross-origin fetch is rejected even from loopback.
func TestPowerOffMethodAndOrigin(t *testing.T) {
	h, _ := newTestHandler(t)
	enablePowerButton(t, h)
	calls := 0
	h.powerOff = func() error { calls++; return nil }
	router := h.Router()

	get := httptest.NewRequest(http.MethodGet, "/poweroff", nil)
	get.RemoteAddr = "127.0.0.1:41234"
	recGet := httptest.NewRecorder()
	router.ServeHTTP(recGet, get)
	if recGet.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /poweroff = %d, want 405", recGet.Code)
	}

	cross := httptest.NewRequest(http.MethodPost, "/poweroff", nil)
	cross.RemoteAddr = "127.0.0.1:41234"
	cross.Header.Set("Origin", "http://evil.example")
	recCross := httptest.NewRecorder()
	router.ServeHTTP(recCross, cross)
	if recCross.Code != http.StatusForbidden {
		t.Fatalf("cross-origin poweroff = %d, want 403", recCross.Code)
	}

	if calls != 0 {
		t.Fatalf("guarded requests reached the seam (%d calls)", calls)
	}
}
