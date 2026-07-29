package display

import (
	"context"
	"errors"
	"testing"
)

// A TV that negotiated badly is the case this feature exists for, so the
// fixture is shaped like one: a connected TV offering several resolutions and
// rates (with the '*'/'+' markers both attached and, on the 1280x720 row,
// detached the way xrandr's column padding sometimes leaves them), an
// interlaced rate that must be skipped rather than guessed at, a second
// connected output, and a disconnected port that must never be offered.
const cannedModes = `Screen 0: minimum 320 x 200, current 1920 x 1080, maximum 16384 x 16384
HDMI-1 connected primary 1024x768+0+0 (normal left inverted right x axis y axis) 1210mm x 680mm
   1920x1080     60.00 +  50.00    59.94
   1280x720      60.00    59.94i
   1024x768      60.00*
eDP-1 connected 1920x1080+1920+0 (normal left inverted right x axis y axis) 344mm x 194mm
   1920x1080     60.05*+
DP-1 disconnected (normal left inverted right x axis y axis)
`

func TestOutputsParsesModesAndMarkers(t *testing.T) {
	stub(t, cannedModes, nil)
	outs, err := Outputs(context.Background())
	if err != nil {
		t.Fatalf("Outputs: %v", err)
	}
	if len(outs) != 3 {
		t.Fatalf("want 3 outputs, got %d: %+v", len(outs), outs)
	}
	hdmi := outs[0]
	if hdmi.Name != "HDMI-1" || !hdmi.Connected {
		t.Fatalf("first output = %+v", hdmi)
	}
	// 3 rates on the 1080 row + 1 usable on the 720 row (59.94i is dropped) + 1
	// on the 768 row.
	if len(hdmi.Modes) != 5 {
		t.Fatalf("want 5 modes, got %d: %+v", len(hdmi.Modes), hdmi.Modes)
	}
	// Detached '+' belongs to the rate before it.
	if m := hdmi.Modes[0]; m.W != 1920 || m.H != 1080 || m.Rate != 60 || !m.Preferred || m.Current {
		t.Errorf("first mode = %+v, want 1920x1080@60 preferred, not current", m)
	}
	if m := hdmi.Modes[1]; m.Rate != 50 || m.Preferred || m.Current {
		t.Errorf("second mode = %+v, want a plain 50Hz", m)
	}
	// The current mode is the one the TV is stuck in, not the preferred one —
	// exactly the situation the picker is for.
	cur, ok := hdmi.CurrentMode()
	if !ok || cur.W != 1024 || cur.H != 768 {
		t.Errorf("CurrentMode = %+v ok=%v, want 1024x768", cur, ok)
	}
	if outs[2].Name != "DP-1" || outs[2].Connected || len(outs[2].Modes) != 0 {
		t.Errorf("disconnected output = %+v", outs[2])
	}
}

// TestOutputsDedupesAddressableDuplicates uses the exact row a real laptop
// panel emits: two 1920x1080 modelines that both round to 60.05. xrandr is
// addressed by resolution+rate, so they are one selectable mode — and the
// picker must not offer the same thing twice under the same stored value.
func TestOutputsDedupesAddressableDuplicates(t *testing.T) {
	stub(t, `Screen 0: minimum 320 x 200, current 3840 x 1080, maximum 16384 x 16384
eDP-1 connected 1920x1080+1920+0 (normal left inverted right x axis y axis) 344mm x 194mm
   1920x1080     60.05*+  60.05    48.04
`, nil)
	outs, err := Outputs(context.Background())
	if err != nil {
		t.Fatalf("Outputs: %v", err)
	}
	ms := outs[0].Modes
	if len(ms) != 2 {
		t.Fatalf("want 2 distinct modes (60.05, 48.04), got %d: %+v", len(ms), ms)
	}
	// The markers were on the first copy; merging must not lose them.
	if !ms[0].Current || !ms[0].Preferred {
		t.Errorf("survivor lost its markers: %+v", ms[0])
	}
	seen := map[string]bool{}
	for _, m := range ms {
		v := FormatSetting("eDP-1", m)
		if seen[v] {
			t.Errorf("duplicate stored value %q", v)
		}
		seen[v] = true
	}
}

func TestOutputsErrorsWithoutX(t *testing.T) {
	// No sysfs fallback here, unlike Displays: setting a mode needs a display
	// server, so an output list nothing could act on would only be a lie.
	stub(t, "", errors.New("no display"))
	if _, err := Outputs(context.Background()); err == nil {
		t.Fatal("want an error when xrandr can't run, got nil")
	}
}

func TestOffers(t *testing.T) {
	stub(t, cannedModes, nil)
	outs, _ := Outputs(context.Background())
	o := outs[0]
	if !o.Offers(Mode{W: 1920, H: 1080, Rate: 60}) {
		t.Error("1920x1080@60 should be offered")
	}
	// Rate is compared with a tolerance — the stored value is rounded to 2dp.
	if !o.Offers(Mode{W: 1920, H: 1080, Rate: 59.94}) {
		t.Error("59.94 should match within tolerance")
	}
	if o.Offers(Mode{W: 3840, H: 2160, Rate: 60}) {
		t.Error("a mode this output never listed must not be offered")
	}
	if o.Offers(Mode{W: 1920, H: 1080, Rate: 24}) {
		t.Error("right resolution, wrong rate must not match")
	}
}

func TestSettingRoundTrip(t *testing.T) {
	m := Mode{W: 1920, H: 1080, Rate: 59.94, Current: true}
	s := FormatSetting("HDMI-1", m)
	if s != "HDMI-1 1920x1080 59.94" {
		t.Fatalf("FormatSetting = %q", s)
	}
	name, got, ok := ParseSetting(s)
	if !ok || name != "HDMI-1" || got.W != 1920 || got.H != 1080 || got.Rate != 59.94 {
		t.Fatalf("ParseSetting(%q) = %q %+v ok=%v", s, name, got, ok)
	}
	// Markers are presentation, not identity — they must not round-trip.
	if got.Current {
		t.Error("parsed mode should carry no current/preferred markers")
	}
}

func TestParseSettingRejectsGarbage(t *testing.T) {
	// hescli writes display_mode with no validation at all, so anything a person
	// can type has to degrade to "no saved mode" rather than half-apply.
	for _, in := range []string{
		"", "HDMI-1", "HDMI-1 1920x1080", "HDMI-1 1920x1080 60 extra",
		"HDMI-1 1920by1080 60", "HDMI-1 axb 60", "HDMI-1 1920x1080 fast",
		"HDMI-1 0x1080 60", "HDMI-1 1920x1080 0", "HDMI-1 -1920x1080 60",
	} {
		if _, _, ok := ParseSetting(in); ok {
			t.Errorf("ParseSetting(%q) accepted a malformed value", in)
		}
	}
}
