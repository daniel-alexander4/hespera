// Package display resolves the physical size of the display an app-mode
// window sits on, so the UI can scale itself to the screen it's actually
// shown on. The server runs on the same machine as the window (app mode),
// which is what makes this a clean read instead of a browser guess: X11's
// xrandr reports every connected output's virtual-desktop rectangle AND its
// physical dimensions in millimetres (from EDID) — a 65" TV and a 24"
// monitor at the same 1080p are trivially distinguishable. Best-effort and
// Linux/X11-shaped: any failure (no xrandr, Wayland without physical info,
// unparseable output) yields "unknown" and the UI keeps its default scale.
package display

import (
	"context"
	"math"
	"os/exec"
	"regexp"
	"strconv"
)

// Scale classes, keyed off the display's physical diagonal. Thresholds:
// under 27" is a desk monitor (the 14px default), 27-45" is a large desktop
// display, over 45" is a TV viewed from across a room.
const (
	ClassDesktop = "desktop"
	ClassLarge   = "large"
	ClassTV      = "tv"

	largeMinInches = 27.0
	tvMinInches    = 45.0
)

// Display is one connected output: its virtual-desktop rectangle in pixels
// and its physical size in millimetres.
type Display struct {
	Name       string
	X, Y, W, H int
	MMW, MMH   int
}

// DiagonalInches is the physical diagonal.
func (d Display) DiagonalInches() float64 {
	return math.Hypot(float64(d.MMW), float64(d.MMH)) / 25.4
}

// Contains reports whether the virtual-desktop point (x,y) is on this display.
func (d Display) Contains(x, y int) bool {
	return x >= d.X && x < d.X+d.W && y >= d.Y && y < d.Y+d.H
}

// classify maps a physical diagonal to a scale class.
func classify(diagInches float64) string {
	switch {
	case diagInches >= tvMinInches:
		return ClassTV
	case diagInches >= largeMinInches:
		return ClassLarge
	default:
		return ClassDesktop
	}
}

// xrandrQuery is the process seam, injectable for tests.
var xrandrQuery = func(ctx context.Context) ([]byte, error) {
	return exec.CommandContext(ctx, "xrandr", "--query").Output()
}

// reConnected matches a connected output line, e.g.
//
//	HDMI-1 connected primary 1920x1080+0+0 (normal ...) 528mm x 297mm
//	eDP-1 connected 1920x1080+1920+0 (normal ...) 344mm x 194mm
//
// Rotation words between the rect and the parenthesis are tolerated.
var reConnected = regexp.MustCompile(`(?m)^(\S+) connected(?: primary)? (\d+)x(\d+)\+(\d+)\+(\d+)[^\n]*? (\d+)mm x (\d+)mm\s*$`)

// Displays returns the connected outputs that carry physical-size info.
// Outputs reporting 0mm (projectors, absent EDID) are skipped — no physical
// info means no scale decision.
func Displays(ctx context.Context) ([]Display, error) {
	out, err := xrandrQuery(ctx)
	if err != nil {
		return nil, err
	}
	var ds []Display
	for _, m := range reConnected.FindAllStringSubmatch(string(out), -1) {
		atoi := func(s string) int { n, _ := strconv.Atoi(s); return n }
		d := Display{
			Name: m[1],
			W:    atoi(m[2]), H: atoi(m[3]),
			X: atoi(m[4]), Y: atoi(m[5]),
			MMW: atoi(m[6]), MMH: atoi(m[7]),
		}
		if d.MMW == 0 || d.MMH == 0 {
			continue
		}
		ds = append(ds, d)
	}
	return ds, nil
}

// ClassAt returns the scale class for the display containing the
// virtual-desktop point (x,y) — typically the app window's screenX/screenY.
// Falls back to the sole display when only one carries physical info (the
// point can be momentarily stale mid-drag). Returns "" when unknown.
func ClassAt(ctx context.Context, x, y int) string {
	ds, err := Displays(ctx)
	if err != nil || len(ds) == 0 {
		return ""
	}
	for _, d := range ds {
		if d.Contains(x, y) {
			return classify(d.DiagonalInches())
		}
	}
	if len(ds) == 1 {
		return classify(ds[0].DiagonalInches())
	}
	return ""
}
