package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"hespera/internal/display"
)

// A TV stuck in the wrong mode: 1024x768 is current, 1920x1080@60 is what it
// actually wants. The second output exists so "wrong output name" is a real
// case rather than an empty list.
func testOutputs() []display.Output {
	return []display.Output{
		{Name: "HDMI-1", Connected: true, Modes: []display.Mode{
			{W: 1920, H: 1080, Rate: 60, Preferred: true},
			{W: 1280, H: 720, Rate: 60},
			{W: 1024, H: 768, Rate: 60, Current: true},
		}},
		{Name: "DP-1", Connected: false},
	}
}

// displayStub wires the three seams and records every mode actually set, so a
// test can assert on what reached xrandr — including the revert.
type displayStub struct {
	mu   sync.Mutex
	sets []string
	err  error
}

func (d *displayStub) install(t *testing.T, h *Handler) {
	t.Helper()
	if _, err := h.db.Exec("INSERT INTO app_settings (key, value) VALUES ('display_control_enabled','1')"); err != nil {
		t.Fatalf("enable display control: %v", err)
	}
	h.displayAvailable = func() (bool, string) { return true, "" }
	h.displayOutputs = func(context.Context) ([]display.Output, error) { return testOutputs(), nil }
	h.displaySetMode = func(_ context.Context, out string, m display.Mode) error {
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.err != nil {
			return d.err
		}
		d.sets = append(d.sets, display.FormatSetting(out, m))
		return nil
	}
}

func (d *displayStub) applied() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.sets...)
}

// shrinkBootWait makes the boot-time poll finish in test time.
func shrinkBootWait(t *testing.T) {
	t.Helper()
	pw, pp := displayBootWait, displayBootPoll
	displayBootWait, displayBootPoll = 60*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { displayBootWait, displayBootPoll = pw, pp })
}

func postMode(t *testing.T, h *Handler, path, remote string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = remote
	rec := httptest.NewRecorder()
	h.Router().ServeHTTP(rec, req)
	return rec
}

// TestDisplayModeLoopbackOnly pins the same contract as the power button: a
// control aimed at the screen attached to the server must never be reachable
// from another device on the network — neither to list modes nor to set one.
func TestDisplayModeLoopbackOnly(t *testing.T) {
	h, _ := newTestHandler(t)
	stub := &displayStub{}
	stub.install(t, h)

	form := url.Values{"mode": {"HDMI-1 1920x1080 60.00"}}
	if rec := postMode(t, h, "/display/mode", "192.168.1.50:4123", form); rec.Code != http.StatusForbidden {
		t.Errorf("LAN apply = %d, want 403", rec.Code)
	}
	if rec := postMode(t, h, "/display/mode/confirm", "192.168.1.50:4123", form); rec.Code != http.StatusForbidden {
		t.Errorf("LAN confirm = %d, want 403", rec.Code)
	}
	req := httptest.NewRequest(http.MethodGet, "/display/modes", nil)
	req.RemoteAddr = "192.168.1.50:4123"
	rec := httptest.NewRecorder()
	h.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("LAN modes = %d, want 403", rec.Code)
	}
	if got := stub.applied(); len(got) != 0 {
		t.Fatalf("a non-loopback request reached xrandr: %v", got)
	}
}

// TestDisplayModeRequiresSetting pins the opt-in: with the setting off (the
// default) even a loopback POST is refused.
func TestDisplayModeRequiresSetting(t *testing.T) {
	h, _ := newTestHandler(t)
	stub := &displayStub{}
	stub.install(t, h)
	if _, err := h.db.Exec("DELETE FROM app_settings WHERE key='display_control_enabled'"); err != nil {
		t.Fatalf("clear setting: %v", err)
	}
	rec := postMode(t, h, "/display/mode", "127.0.0.1:4123", url.Values{"mode": {"HDMI-1 1920x1080 60.00"}})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("apply with the setting off = %d, want 403", rec.Code)
	}
	if got := stub.applied(); len(got) != 0 {
		t.Fatalf("xrandr reached with the setting off: %v", got)
	}
}

// TestDisplayModeRejectsUnofferedMode: the picker's list is built when the card
// is opened, so a screen can be swapped before the button is pressed. The
// server re-checks rather than trusting the page.
func TestDisplayModeRejectsUnofferedMode(t *testing.T) {
	h, _ := newTestHandler(t)
	stub := &displayStub{}
	stub.install(t, h)

	for _, tc := range []struct{ name, mode string }{
		{"never offered", "HDMI-1 3840x2160 60.00"},
		{"unknown output", "HDMI-9 1920x1080 60.00"},
		{"disconnected output", "DP-1 1920x1080 60.00"},
		{"malformed", "not a mode"},
	} {
		rec := postMode(t, h, "/display/mode", "127.0.0.1:4123", url.Values{"mode": {tc.mode}})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: got %d, want 400", tc.name, rec.Code)
		}
	}
	if got := stub.applied(); len(got) != 0 {
		t.Fatalf("a rejected mode still reached xrandr: %v", got)
	}
}

// TestDisplayModeRevertsWithoutConfirmation is the safety property: an applied
// mode is provisional, and the SERVER puts it back. Nothing the page does (or
// fails to do, having gone dark) is involved.
func TestDisplayModeRevertsWithoutConfirmation(t *testing.T) {
	h, _ := newTestHandler(t)
	stub := &displayStub{}
	stub.install(t, h)

	rec := postMode(t, h, "/display/mode", "127.0.0.1:4123", url.Values{"mode": {"HDMI-1 1920x1080 60.00"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("apply = %d (%s)", rec.Code, rec.Body.String())
	}
	if got := stub.applied(); len(got) != 1 || got[0] != "HDMI-1 1920x1080 60.00" {
		t.Fatalf("applied = %v", got)
	}
	// Nothing is stored until it's confirmed — an unconfirmed mode must not
	// survive to be re-applied at the next boot.
	if v := h.effectiveDisplayMode(context.Background()); v != "" {
		t.Fatalf("unconfirmed mode was persisted: %q", v)
	}

	// Fire the revert directly rather than waiting out the real window.
	h.revertDisplayMode("HDMI-1 1920x1080 60.00")
	got := stub.applied()
	if len(got) != 2 || got[1] != "HDMI-1 1024x768 60.00" {
		t.Fatalf("revert should restore the mode the screen was in, got %v", got)
	}
	if v := h.effectiveDisplayMode(context.Background()); v != "" {
		t.Fatalf("revert must not persist anything, got %q", v)
	}
}

// TestDisplayModeKeepPersists: confirming disarms the revert and stores the
// mode — the only path that writes display_mode, which is what makes the
// unattended boot-time re-apply safe.
func TestDisplayModeKeepPersists(t *testing.T) {
	h, _ := newTestHandler(t)
	stub := &displayStub{}
	stub.install(t, h)

	postMode(t, h, "/display/mode", "127.0.0.1:4123", url.Values{"mode": {"HDMI-1 1920x1080 60.00"}})
	rec := postMode(t, h, "/display/mode/confirm", "127.0.0.1:4123",
		url.Values{"mode": {"HDMI-1 1920x1080 60.00"}, "keep": {"1"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("keep = %d (%s)", rec.Code, rec.Body.String())
	}
	if v := h.effectiveDisplayMode(context.Background()); v != "HDMI-1 1920x1080 60.00" {
		t.Fatalf("saved mode = %q", v)
	}
	// A timer that fires after the keep must not undo a mode the user is
	// watching — the Up Next lesson, re-checked here.
	h.revertDisplayMode("HDMI-1 1920x1080 60.00")
	if got := stub.applied(); len(got) != 1 {
		t.Fatalf("a stale revert undid a confirmed mode: %v", got)
	}
}

// TestDisplayModeConfirmRejectsStale: a page confirming a change that has
// already been undone must not store a mode nobody is looking at.
func TestDisplayModeConfirmRejectsStale(t *testing.T) {
	h, _ := newTestHandler(t)
	stub := &displayStub{}
	stub.install(t, h)

	postMode(t, h, "/display/mode", "127.0.0.1:4123", url.Values{"mode": {"HDMI-1 1920x1080 60.00"}})
	h.revertDisplayMode("HDMI-1 1920x1080 60.00") // the window lapsed

	rec := postMode(t, h, "/display/mode/confirm", "127.0.0.1:4123",
		url.Values{"mode": {"HDMI-1 1920x1080 60.00"}, "keep": {"1"}})
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale keep = %d, want 409", rec.Code)
	}
	if v := h.effectiveDisplayMode(context.Background()); v != "" {
		t.Fatalf("stale keep persisted %q", v)
	}
}

// TestDisplayModeCancelRestoresImmediately: "Go back" doesn't wait out the
// window.
func TestDisplayModeCancelRestoresImmediately(t *testing.T) {
	h, _ := newTestHandler(t)
	stub := &displayStub{}
	stub.install(t, h)

	postMode(t, h, "/display/mode", "127.0.0.1:4123", url.Values{"mode": {"HDMI-1 1280x720 60.00"}})
	rec := postMode(t, h, "/display/mode/confirm", "127.0.0.1:4123",
		url.Values{"mode": {"HDMI-1 1280x720 60.00"}, "keep": {"0"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel = %d", rec.Code)
	}
	got := stub.applied()
	if len(got) != 2 || got[1] != "HDMI-1 1024x768 60.00" {
		t.Fatalf("cancel should restore at once, got %v", got)
	}
}

// TestDisplayModeOneChangeAtATime: a second apply while one is unconfirmed is
// refused, or its revert would point at the first change's mode instead of the
// mode the screen was originally in.
func TestDisplayModeOneChangeAtATime(t *testing.T) {
	h, _ := newTestHandler(t)
	stub := &displayStub{}
	stub.install(t, h)

	postMode(t, h, "/display/mode", "127.0.0.1:4123", url.Values{"mode": {"HDMI-1 1920x1080 60.00"}})
	rec := postMode(t, h, "/display/mode", "127.0.0.1:4123", url.Values{"mode": {"HDMI-1 1280x720 60.00"}})
	if rec.Code != http.StatusConflict {
		t.Fatalf("second apply = %d, want 409", rec.Code)
	}
	if got := stub.applied(); len(got) != 1 {
		t.Fatalf("second apply reached xrandr: %v", got)
	}
}

// TestApplySavedDisplayModeGuards: the boot-time re-apply runs with nobody
// there to confirm it, so it must skip a mode the screen no longer advertises
// rather than forcing it.
func TestApplySavedDisplayModeGuards(t *testing.T) {
	for _, tc := range []struct {
		name  string
		saved string
		want  int
	}{
		{"offered mode is applied", "HDMI-1 1920x1080 60.00", 1},
		{"mode no longer offered is skipped", "HDMI-1 3840x2160 60.00", 0},
		{"unknown output is skipped", "HDMI-9 1920x1080 60.00", 0},
		{"garbage is ignored", "nonsense", 0},
		{"nothing saved", "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := newTestHandler(t)
			stub := &displayStub{}
			stub.install(t, h)
			// An absent output legitimately polls until the deadline (it may be
			// plugged in late during boot); shrink the wait rather than sit it out.
			shrinkBootWait(t)
			if tc.saved != "" {
				if _, err := h.db.Exec("INSERT INTO app_settings (key, value) VALUES ('display_mode',?)", tc.saved); err != nil {
					t.Fatalf("seed: %v", err)
				}
			}
			done := make(chan struct{})
			go func() { h.applySavedDisplayMode(); close(done) }()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("applySavedDisplayMode did not return")
			}
			if got := stub.applied(); len(got) != tc.want {
				t.Fatalf("applied %v, want %d call(s)", got, tc.want)
			}
		})
	}
}
