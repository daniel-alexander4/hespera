//go:build linux

package browser

import "testing"

// The pointer-inside test is what decides whether FocusWindow warps the pointer
// at all. Getting it wrong in the "already inside" direction would fire a
// mousemove at the page on every launch, flipping the UI into mouse modality and
// suppressing the remote's focus ring — so it is worth pinning.
func TestRectContains(t *testing.T) {
	// A maximized window on the right-hand monitor of a dual-head desktop.
	win := rect{X: 1920, Y: 0, W: 1920, H: 1080}

	tests := []struct {
		name   string
		px, py int
		want   bool
	}{
		{"pointer in the middle of the window", 2880, 540, true},
		{"top-left corner is inside", 1920, 0, true},
		{"bottom-right corner is outside (half-open)", 3840, 1080, false},
		{"last pixel inside", 3839, 1079, true},
		{"pointer on the other monitor", 500, 400, false},
		{"same x, above the window", 2880, -1, false},
		{"same y, left of the window", 1919, 540, false},
	}
	for _, tt := range tests {
		if got := win.contains(tt.px, tt.py); got != tt.want {
			t.Errorf("%s: contains(%d,%d) = %v, want %v", tt.name, tt.px, tt.py, got, tt.want)
		}
	}
}

func TestShellVars(t *testing.T) {
	// xdotool getwindowgeometry --shell
	kv := shellVars("WINDOW=48234498\nX=1920\nY=0\nWIDTH=1920\nHEIGHT=1080\nSCREEN=0\n")
	for k, want := range map[string]int{"X": 1920, "Y": 0, "WIDTH": 1920, "HEIGHT": 1080} {
		if kv[k] != want {
			t.Errorf("shellVars[%q] = %d, want %d", k, kv[k], want)
		}
	}
	// Garbage and blank lines are skipped, not fatal — a missing key reads 0,
	// which fails contains() harmlessly rather than warping to a bogus point.
	kv = shellVars("X=notanumber\n\nY=42\nnonsense\n")
	if _, ok := kv["X"]; ok {
		t.Error("unparseable value should be dropped, not stored")
	}
	if kv["Y"] != 42 {
		t.Errorf("Y = %d, want 42", kv["Y"])
	}
}
