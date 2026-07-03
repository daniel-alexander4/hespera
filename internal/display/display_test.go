package display

import (
	"context"
	"errors"
	"testing"
)

// Canned xrandr output from a real two-display box: a 23.9" HDMI monitor at
// the virtual-desktop origin and a 15.5" laptop panel to its right, plus a
// disconnected port and a connected output with no EDID physical size.
const cannedXrandr = `Screen 0: minimum 320 x 200, current 3840 x 1080, maximum 16384 x 16384
HDMI-1 connected primary 1920x1080+0+0 (normal left inverted right x axis y axis) 528mm x 297mm
   1920x1080     60.00*+
eDP-1 connected 1920x1080+1920+0 (normal left inverted right x axis y axis) 344mm x 194mm
   1920x1080     60.05*+
DP-1 disconnected (normal left inverted right x axis y axis)
DP-2 connected 1280x720+3840+0 (normal left inverted right x axis y axis) 0mm x 0mm
`

func stub(t *testing.T, out string, err error) {
	t.Helper()
	prev := xrandrQuery
	xrandrQuery = func(context.Context) ([]byte, error) { return []byte(out), err }
	t.Cleanup(func() { xrandrQuery = prev })
}

func TestDisplaysParsesConnectedWithPhysicalSize(t *testing.T) {
	stub(t, cannedXrandr, nil)
	ds, err := Displays(context.Background())
	if err != nil {
		t.Fatalf("Displays: %v", err)
	}
	if len(ds) != 2 {
		t.Fatalf("got %d displays, want 2 (disconnected + 0mm skipped): %+v", len(ds), ds)
	}
	hdmi := ds[0]
	if hdmi.Name != "HDMI-1" || hdmi.X != 0 || hdmi.W != 1920 || hdmi.MMW != 528 {
		t.Fatalf("HDMI-1 parsed wrong: %+v", hdmi)
	}
	if d := hdmi.DiagonalInches(); d < 23.5 || d > 24.3 {
		t.Fatalf("HDMI-1 diagonal = %.1f, want ~23.9", d)
	}
	if ds[1].Name != "eDP-1" || ds[1].X != 1920 {
		t.Fatalf("eDP-1 parsed wrong: %+v", ds[1])
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		diag float64
		want string
	}{
		{15.5, ClassDesktop},
		{23.9, ClassDesktop},
		{26.9, ClassDesktop},
		{27.0, ClassLarge},
		{44.9, ClassLarge},
		{45.0, ClassTV},
		{65.0, ClassTV},
	}
	for _, c := range cases {
		if got := classify(c.diag); got != c.want {
			t.Fatalf("classify(%.1f) = %q, want %q", c.diag, got, c.want)
		}
	}
}

func TestClassAt(t *testing.T) {
	stub(t, cannedXrandr, nil)
	ctx := context.Background()

	// Point on the HDMI monitor (23.9" → desktop).
	if got := ClassAt(ctx, 100, 100); got != ClassDesktop {
		t.Fatalf("ClassAt(HDMI point) = %q, want desktop", got)
	}
	// Point on the laptop panel.
	if got := ClassAt(ctx, 2000, 500); got != ClassDesktop {
		t.Fatalf("ClassAt(eDP point) = %q, want desktop", got)
	}
	// Point outside every rect with two candidates → unknown.
	if got := ClassAt(ctx, 99999, 99999); got != "" {
		t.Fatalf("ClassAt(nowhere) = %q, want unknown", got)
	}
}

func TestClassAtTVSize(t *testing.T) {
	// A 65" TV: 1430x800mm.
	stub(t, "HDMI-1 connected 3840x2160+0+0 (normal) 1430mm x 800mm\n", nil)
	if got := ClassAt(context.Background(), 10, 10); got != ClassTV {
		t.Fatalf("ClassAt(65\" TV) = %q, want tv", got)
	}
}

func TestClassAtSingleDisplayFallback(t *testing.T) {
	// Stale point off-rect, but only one display exists → use it.
	stub(t, "eDP-1 connected 1920x1080+0+0 (normal) 344mm x 194mm\n", nil)
	if got := ClassAt(context.Background(), -50, -50); got != ClassDesktop {
		t.Fatalf("single-display fallback = %q, want desktop", got)
	}
}

func TestClassAtUnavailable(t *testing.T) {
	stub(t, "", errors.New("no xrandr"))
	if got := ClassAt(context.Background(), 0, 0); got != "" {
		t.Fatalf("ClassAt(no xrandr) = %q, want unknown", got)
	}
	stub(t, "garbage with no outputs\n", nil)
	if got := ClassAt(context.Background(), 0, 0); got != "" {
		t.Fatalf("ClassAt(garbage) = %q, want unknown", got)
	}
}
