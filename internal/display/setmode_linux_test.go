//go:build linux

package display

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// stubSet swaps the xrandr process seam and records the arguments. No test in
// this package may ever reach the real binary — it would change the screen of
// whoever is running the suite.
func stubSet(t *testing.T, out string, err error) *[]string {
	t.Helper()
	var got []string
	prev := xrandrSet
	xrandrSet = func(_ context.Context, args ...string) ([]byte, error) {
		got = args
		return []byte(out), err
	}
	t.Cleanup(func() { xrandrSet = prev })
	return &got
}

func TestSetModeArgs(t *testing.T) {
	got := stubSet(t, "", nil)
	if err := SetMode(context.Background(), "HDMI-1", Mode{W: 1920, H: 1080, Rate: 59.94}); err != nil {
		t.Fatalf("SetMode: %v", err)
	}
	want := "--output HDMI-1 --mode 1920x1080 --rate 59.94"
	if strings.Join(*got, " ") != want {
		t.Errorf("args = %q, want %q", strings.Join(*got, " "), want)
	}
}

func TestSetModeReportsXrandrOutput(t *testing.T) {
	// xrandr explains a rejected mode on stderr, and that text is what the
	// settings page shows — so it has to survive into the error.
	stubSet(t, "xrandr: cannot find mode 1920x1080", errors.New("exit status 1"))
	err := SetMode(context.Background(), "HDMI-1", Mode{W: 1920, H: 1080, Rate: 60})
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "cannot find mode") {
		t.Errorf("error lost xrandr's explanation: %v", err)
	}
}

func TestAvailableGates(t *testing.T) {
	t.Setenv("HESPERA_NO_DISPLAY_CONTROL", "1")
	t.Setenv("DISPLAY", ":0")
	if ok, reason := Available(); ok || !strings.Contains(reason, "HESPERA_NO_DISPLAY_CONTROL") {
		t.Errorf("kill switch not honored: ok=%v reason=%q", ok, reason)
	}

	t.Setenv("HESPERA_NO_DISPLAY_CONTROL", "")
	t.Setenv("DISPLAY", "")
	ok, reason := Available()
	if ok {
		t.Error("want unavailable with no DISPLAY")
	}
	// The reason is rendered verbatim in Settings, so it has to name the fix —
	// this is the exact state a session-less systemd service is in.
	if !strings.Contains(reason, "/etc/default/hespera") {
		t.Errorf("reason should tell the operator how to fix it, got %q", reason)
	}
}
