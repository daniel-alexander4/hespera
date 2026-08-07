package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// noiseTestController gives each test its own config file, so a save in one
// cannot leak into another.
func noiseTestController(t *testing.T) *controller {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	return newController(newClient("http://127.0.0.1:1"), engine{name: "mpv", path: "/nonexistent/mpv"})
}

func doJSON(t *testing.T, h http.Handler, method, path, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w, out
}

func TestNoiseAPIGetReturnsDefaults(t *testing.T) {
	ct := noiseTestController(t)
	w, body := doJSON(t, http.HandlerFunc(ct.handleNoise), http.MethodGet, "/api/noise", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	presets, _ := body["presets"].([]any)
	if len(presets) != len(defaultNoiseConfig().Presets) {
		t.Fatalf("expected the built-in presets, got %d", len(presets))
	}
	if body["default"] != "brown" {
		t.Errorf("default preset: got %v", body["default"])
	}
	if _, ok := body["available"]; !ok {
		t.Error("available missing: the UI needs it to hide controls that can only fail")
	}
}

// TestNoiseAPIRejectsBadSchedule: a window is unattended, so an unusable value
// must fail at the point someone types it rather than as a silent night.
func TestNoiseAPIRejectsBadSchedule(t *testing.T) {
	ct := noiseTestController(t)
	h := http.HandlerFunc(ct.handleNoise)

	bad := []struct {
		name string
		body string
	}{
		{"hour out of range", `{"presets":[{"name":"brown","type":"brownnoise"}],"schedule":[{"start":"25:00","end":"10:00"}]}`},
		{"not a time at all", `{"presets":[{"name":"brown","type":"brownnoise"}],"schedule":[{"start":"8pm","end":"10:00"}]}`},
		{"weekday out of range", `{"presets":[{"name":"brown","type":"brownnoise"}],"schedule":[{"start":"20:00","end":"10:00","days":[9]}]}`},
		{"window names a preset that does not exist", `{"presets":[{"name":"brown","type":"brownnoise"}],"schedule":[{"start":"20:00","end":"10:00","preset":"purple"}]}`},
		{"unknown waveform", `{"presets":[{"name":"x","type":"greennoise"}]}`},
		{"preset with no name", `{"presets":[{"name":"  ","type":"brownnoise"}]}`},
		{"no presets at all", `{"presets":[]}`},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			w, _ := doJSON(t, h, http.MethodPut, "/api/noise", c.body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("got %d, want 400: %s", w.Code, w.Body)
			}
		})
	}

	// And nothing was written: a rejected save must not half-apply.
	_, body := doJSON(t, h, http.MethodGet, "/api/noise", "")
	presets, _ := body["presets"].([]any)
	if len(presets) != len(defaultNoiseConfig().Presets) {
		t.Fatalf("a rejected PUT changed the stored config (%d presets)", len(presets))
	}
}

func TestNoiseAPIRoundTrip(t *testing.T) {
	ct := noiseTestController(t)
	h := http.HandlerFunc(ct.handleNoise)

	put := `{"audioDev":"plughw:0","default":"brown","presets":[{"name":"brown","type":"brownnoise","waveSpeed":0.1,"waveDepth":40}],` +
		`"schedule":[{"start":"20:00","end":"10:00","days":[0,6],"preset":"brown"}]}`
	if w, _ := doJSON(t, h, http.MethodPut, "/api/noise", put); w.Code != http.StatusOK {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}

	_, body := doJSON(t, h, http.MethodGet, "/api/noise", "")
	if body["audioDev"] != "plughw:0" {
		t.Errorf("audioDev: got %v", body["audioDev"])
	}
	sched, _ := body["schedule"].([]any)
	if len(sched) != 1 {
		t.Fatalf("schedule: got %v", body["schedule"])
	}
	win, _ := sched[0].(map[string]any)
	if win["start"] != "20:00" || win["end"] != "10:00" {
		t.Errorf("window round trip: %v", win)
	}

	// Saved values are normalized on the way in, so what comes back is what will
	// actually be played rather than what was typed.
	presets, _ := body["presets"].([]any)
	p, _ := presets[0].(map[string]any)
	if p["centerHz"] == nil || p["centerHz"].(float64) != 1786 {
		t.Errorf("omitted fields should be filled with the original's values, got %v", p["centerHz"])
	}
}

// TestNoiseAPIClampsOnSave: a value out of range is clamped rather than
// refused, so a slider dragged to an extreme still yields usable noise.
func TestNoiseAPIClampsOnSave(t *testing.T) {
	ct := noiseTestController(t)
	h := http.HandlerFunc(ct.handleNoise)
	put := `{"presets":[{"name":"brown","type":"brownnoise","waveSpeed":9,"gainDb":999}]}`
	if w, _ := doJSON(t, h, http.MethodPut, "/api/noise", put); w.Code != http.StatusOK {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	_, body := doJSON(t, h, http.MethodGet, "/api/noise", "")
	presets, _ := body["presets"].([]any)
	p, _ := presets[0].(map[string]any)
	if p["waveSpeed"].(float64) != noiseMaxWaveSpeed {
		t.Errorf("waveSpeed should clamp to %g, got %v", noiseMaxWaveSpeed, p["waveSpeed"])
	}
	if p["gainDb"].(float64) != 20 {
		t.Errorf("gainDb should clamp to 20, got %v", p["gainDb"])
	}
}

// TestStopByHumanLatchesOnlyInsideAWindow is the distinction the whole schedule
// design rests on: a human's stop suppresses the rest of the window, while the
// same stop outside one leaves nothing behind to block the next night.
func TestStopByHumanLatchesOnlyInsideAWindow(t *testing.T) {
	ct := noiseTestController(t)
	cfg := defaultNoiseConfig()
	cfg.Schedule = []noiseWindow{{Start: "20:00", End: "10:00", Preset: "brown"}}
	if err := saveNoiseConfig(cfg); err != nil {
		t.Fatal(err)
	}

	// Inside a window, stopping noise latches until the window ends.
	ct.mu.Lock()
	ct.kind = sessionNoise
	ct.mu.Unlock()
	ct.stopByHuman(at(sunday, 23, 0))
	if got := ct.runtime().LatchUntil; !got.Equal(at(monday, 10, 0)) {
		t.Errorf("latch: got %v, want the window end %v", got, at(monday, 10, 0))
	}

	// Outside a window there is nothing to suppress.
	ct2 := noiseTestController(t)
	if err := saveNoiseConfig(cfg); err != nil {
		t.Fatal(err)
	}
	ct2.mu.Lock()
	ct2.kind = sessionNoise
	ct2.mu.Unlock()
	ct2.stopByHuman(at(monday, 12, 0))
	if got := ct2.runtime().LatchUntil; !got.IsZero() {
		t.Errorf("stopping outside a window should set no latch, got %v", got)
	}
}

// TestStopByHumanIgnoresAQueue: pressing stop on music must not latch the noise
// schedule off — they are unrelated gestures that share one button.
func TestStopByHumanIgnoresAQueue(t *testing.T) {
	ct := noiseTestController(t)
	cfg := defaultNoiseConfig()
	cfg.Schedule = []noiseWindow{{Start: "20:00", End: "10:00", Preset: "brown"}}
	if err := saveNoiseConfig(cfg); err != nil {
		t.Fatal(err)
	}
	ct.mu.Lock()
	ct.kind = sessionQueue
	ct.mu.Unlock()

	ct.stopByHuman(at(sunday, 23, 0))
	if got := ct.runtime().LatchUntil; !got.IsZero() {
		t.Errorf("stopping music latched the noise schedule off until %v", got)
	}
}

// TestNoiseSessionReportsAsPlaying: a noise session has no actions channel, so
// the "is something playing" test had to move to the session kind. If it
// regresses, the remote shows idle while the box roars.
func TestNoiseSessionReportsAsPlaying(t *testing.T) {
	ct := noiseTestController(t)
	ct.mu.Lock()
	ct.kind = sessionNoise
	ct.state = nowPlaying{Queue: "Noise", Title: "brown noise"}
	ct.mu.Unlock()

	w, body := doJSON(t, http.HandlerFunc(ct.handleState), http.MethodGet, "/api/state", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	if body["playing"] != true {
		t.Fatalf("playing: got %v, want true", body["playing"])
	}
}

// TestNoiseStateReportsKindAndPausability: the remote renders a noise session
// differently from a queue (footer on every screen, no next/previous), and it
// learns which from the state's kind. canPause must follow the signal seam,
// not the music engine's IPC — mpv's socket says nothing about SoX.
func TestNoiseStateReportsKindAndPausability(t *testing.T) {
	ct := noiseTestController(t)
	ct.mu.Lock()
	ct.kind = sessionNoise
	ct.state = nowPlaying{Queue: "Noise", Title: "brown noise", Index: 1, Total: 1}
	ct.mu.Unlock()

	// Before the engine process exists there is nothing to signal.
	_, body := doJSON(t, http.HandlerFunc(ct.handleState), http.MethodGet, "/api/state", "")
	if body["kind"] != "noise" {
		t.Fatalf("kind: got %v, want noise", body["kind"])
	}
	if body["canPause"] != false {
		t.Errorf("canPause with no engine yet: got %v, want false", body["canPause"])
	}

	ct.mu.Lock()
	ct.noisePause = func(bool) error { return nil }
	ct.mu.Unlock()
	_, body = doJSON(t, http.HandlerFunc(ct.handleState), http.MethodGet, "/api/state", "")
	if body["canPause"] != true {
		t.Errorf("canPause with the seam installed: got %v, want true", body["canPause"])
	}
}

// TestPauseRoutesNoiseThroughTheSignalSeam: a noise session has no track id and
// no IPC socket, so /api/pause must take the signal path — before this, the
// trackID guard 409'd every noise pause as "nothing is playing".
func TestPauseRoutesNoiseThroughTheSignalSeam(t *testing.T) {
	ct := noiseTestController(t)
	var got []bool
	ct.mu.Lock()
	ct.kind = sessionNoise
	ct.state = nowPlaying{Queue: "Noise", Title: "brown noise", Index: 1, Total: 1}
	ct.noisePause = func(pause bool) error { got = append(got, pause); return nil }
	ct.mu.Unlock()

	h := http.HandlerFunc(ct.handlePause)
	w, body := doJSON(t, h, http.MethodPost, "/api/pause", `{"paused":true}`)
	if w.Code != http.StatusOK || body["paused"] != true {
		t.Fatalf("pause: %d %s", w.Code, w.Body)
	}
	if !ct.paused.Load() {
		t.Error("the paused flag did not follow a successful signal")
	}
	w, body = doJSON(t, h, http.MethodPost, "/api/pause", `{"paused":false}`)
	if w.Code != http.StatusOK || body["paused"] != false {
		t.Fatalf("resume: %d %s", w.Code, w.Body)
	}
	if len(got) != 2 || got[0] != true || got[1] != false {
		t.Fatalf("signals sent: %v, want [true false]", got)
	}
}

// TestPauseNoiseFailureClaimsNothing: a failed signal must leave the paused
// flag alone — the remote's glyph follows that flag, and a lying flag would
// show a paused bar over a box still making noise.
func TestPauseNoiseFailureClaimsNothing(t *testing.T) {
	ct := noiseTestController(t)
	ct.mu.Lock()
	ct.kind = sessionNoise
	ct.state = nowPlaying{Queue: "Noise", Title: "brown noise", Index: 1, Total: 1}
	ct.noisePause = func(bool) error { return errors.New("no such process") }
	ct.mu.Unlock()

	w, _ := doJSON(t, http.HandlerFunc(ct.handlePause), http.MethodPost, "/api/pause", `{"paused":true}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("got %d, want 409", w.Code)
	}
	if ct.paused.Load() {
		t.Error("a failed signal set the paused flag anyway")
	}
}

func TestNoiseAPIMethodGuards(t *testing.T) {
	ct := noiseTestController(t)
	if w, _ := doJSON(t, http.HandlerFunc(ct.handleNoise), http.MethodDelete, "/api/noise", ""); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("DELETE /api/noise: got %d", w.Code)
	}
	if w, _ := doJSON(t, http.HandlerFunc(ct.handleNoiseStart), http.MethodGet, "/api/noise/start", ""); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /api/noise/start: got %d", w.Code)
	}
}

// TestNoiseStartRejectsUnknownPreset — the id/name confusion that bit the play
// path is not possible here, but a typo still must not start something random.
func TestNoiseStartRejectsUnknownPreset(t *testing.T) {
	ct := noiseTestController(t)
	w, _ := doJSON(t, http.HandlerFunc(ct.handleNoiseStart), http.MethodPost, "/api/noise/start", `{"preset":"purple"}`)
	// 400 when sox is present, 503 when it is not; either way it did not start.
	if w.Code != http.StatusBadRequest && w.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d, want a refusal: %s", w.Code, w.Body)
	}
	if ct.runtime().Kind != sessionNone {
		t.Fatal("an unknown preset started a session")
	}
}
