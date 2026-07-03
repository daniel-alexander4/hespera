package display

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
	// Keep the DRM fallback hermetic too — an xrandr-failure test must not
	// discover the dev box's real connectors.
	prevRoot := drmRoot
	drmRoot = t.TempDir()
	t.Cleanup(func() { drmRoot = prevRoot })
}

// fakeConnector writes one connector dir under the (test-owned) drmRoot.
func fakeConnector(t *testing.T, name, status, enabled string, edid []byte) {
	t.Helper()
	dir := filepath.Join(drmRoot, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for f, v := range map[string][]byte{"status": []byte(status + "\n"), "enabled": []byte(enabled + "\n"), "edid": edid} {
		if err := os.WriteFile(filepath.Join(dir, f), v, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// realEDID loads one of the captured-from-hardware fixtures.
func realEDID(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// tvEDID returns a real EDID re-sized to TV dimensions (bytes 21/22 are the
// physical size in cm; our parser reads the header + size, not the checksum).
func tvEDID(t *testing.T) []byte {
	e := append([]byte(nil), realEDID(t, "edid-hdmi-24in.bin")...)
	e[21], e[22] = 121, 68 // 1210×680mm ≈ 54.7" → tv
	return e
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

// The DRM sysfs fallback — the pure-Wayland / no-XWayland path. Uses EDID
// blobs captured from real hardware (testdata/) plus a TV-resized variant.

func TestDRMFallbackSingleTV(t *testing.T) {
	stub(t, "", errors.New("no X server")) // xrandr dead → DRM path
	fakeConnector(t, "card0-HDMI-A-1", "connected", "enabled", tvEDID(t))
	if got := ClassAt(context.Background(), 0, 0); got != ClassTV {
		t.Fatalf("single TV connector = %q, want tv", got)
	}
}

func TestDRMFallbackFiresWhenXrandrHasNoPhysicalMM(t *testing.T) {
	// XWayland alive but reporting 0mm — the other Wayland failure mode.
	stub(t, "XWAYLAND0 connected 3840x2160+0+0 (normal) 0mm x 0mm\n", nil)
	fakeConnector(t, "card0-HDMI-A-1", "connected", "enabled", tvEDID(t))
	if got := ClassAt(context.Background(), 0, 0); got != ClassTV {
		t.Fatalf("0mm-xrandr + TV connector = %q, want tv", got)
	}
}

func TestDRMFallbackRealEDIDs(t *testing.T) {
	stub(t, "", errors.New("no X server"))
	fakeConnector(t, "card1-eDP-1", "connected", "enabled", realEDID(t, "edid-edp-15in.bin"))
	fakeConnector(t, "card1-HDMI-A-1", "connected", "enabled", realEDID(t, "edid-hdmi-24in.bin"))

	ds, err := Displays(context.Background())
	if err != nil || len(ds) != 2 {
		t.Fatalf("got %d displays (err %v), want 2", len(ds), err)
	}
	sizes := map[string][2]int{}
	for _, d := range ds {
		sizes[d.Name] = [2]int{d.MMW, d.MMH}
	}
	if sizes["card1-eDP-1"] != [2]int{340, 190} {
		t.Fatalf("eDP EDID size = %v, want 340x190mm", sizes["card1-eDP-1"])
	}
	if sizes["card1-HDMI-A-1"] != [2]int{530, 300} {
		t.Fatalf("HDMI EDID size = %v, want 530x300mm", sizes["card1-HDMI-A-1"])
	}
	// Two connectors, no geometry → which one holds the window is unknowable.
	if got := ClassAt(context.Background(), 0, 0); got != "" {
		t.Fatalf("two DRM connectors = %q, want unknown", got)
	}
}

func TestDRMFallbackSkipsUnusableConnectors(t *testing.T) {
	stub(t, "", errors.New("no X server"))
	fakeConnector(t, "card0-DP-1", "disconnected", "disabled", nil)                            // not connected
	fakeConnector(t, "card0-eDP-1", "connected", "disabled", realEDID(t, "edid-edp-15in.bin")) // lid closed
	fakeConnector(t, "card0-DP-2", "connected", "enabled", []byte("not an edid blob"))         // garbage EDID
	zero := tvEDID(t)
	zero[21], zero[22] = 0, 0
	fakeConnector(t, "card0-DP-3", "connected", "enabled", zero) // projector: size undefined
	fakeConnector(t, "card0-HDMI-A-1", "connected", "enabled", tvEDID(t))

	ds, _ := Displays(context.Background())
	if len(ds) != 1 || ds[0].Name != "card0-HDMI-A-1" {
		t.Fatalf("want only the TV connector to survive, got %+v", ds)
	}
	if got := ClassAt(context.Background(), 0, 0); got != ClassTV {
		t.Fatalf("sole usable connector = %q, want tv", got)
	}
}
