package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestShutdownLoopbackOnly pins the LAN-serving safety contract: the power
// button quits only from the machine Hespera runs on. A household device's
// tap (any non-loopback RemoteAddr) is refused and must not trigger quit.
func TestShutdownLoopbackOnly(t *testing.T) {
	h, _ := newTestHandler(t)
	quitCh := make(chan struct{}, 2)
	h.quit = func() { quitCh <- struct{}{} }
	router := h.Router()

	post := func(remoteAddr string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/shutdown", nil)
		req.RemoteAddr = remoteAddr
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	// Remote LAN device → refused, no quit.
	if rec := post("192.168.1.50:41234"); rec.Code != http.StatusForbidden {
		t.Fatalf("LAN shutdown = %d, want 403", rec.Code)
	}
	select {
	case <-quitCh:
		t.Fatal("LAN request triggered quit")
	case <-time.After(100 * time.Millisecond):
	}

	// IPv6 non-loopback → refused.
	if rec := post("[2001:db8::7]:41234"); rec.Code != http.StatusForbidden {
		t.Fatalf("IPv6 LAN shutdown = %d, want 403", rec.Code)
	}

	// Loopback (v4 and v6) → allowed.
	if rec := post("127.0.0.1:41234"); rec.Code != http.StatusOK {
		t.Fatalf("loopback shutdown = %d, want 200", rec.Code)
	}
	if rec := post("[::1]:41234"); rec.Code != http.StatusOK {
		t.Fatalf("IPv6 loopback shutdown = %d, want 200", rec.Code)
	}
	select {
	case <-quitCh:
	case <-time.After(2 * time.Second):
		t.Fatal("loopback request did not trigger quit")
	}
}
