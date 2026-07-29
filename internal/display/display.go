// Package display resolves the physical size of the display an app-mode
// window sits on, so the UI can scale itself to the screen it's actually
// shown on. The server runs on the same machine as the window (app mode),
// which is what makes this a clean read instead of a browser guess: X11's
// xrandr reports every connected output's virtual-desktop rectangle AND its
// physical dimensions in millimetres (from EDID) — a 65" TV and a 24"
// monitor at the same 1080p are trivially distinguishable. When xrandr
// yields nothing usable (pure Wayland with no XWayland, or a compositor
// that reports no physical mm), the /sys/class/drm EDID fallback reads each
// connected connector's physical size straight from the kernel — sizes but
// no positions (no display server tells a Wayland client where its window
// is), so a single connected display classifies and several stay unknown.
// Best-effort throughout: any failure yields "unknown" and the UI keeps its
// default scale.
package display

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
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
// info means no scale decision. When xrandr fails or reports no usable
// output, the DRM sysfs fallback supplies sizes without geometry.
func Displays(ctx context.Context) ([]Display, error) {
	out, err := xrandrQuery(ctx)
	if err != nil {
		return drmDisplays(), nil // pure Wayland / no X server: sysfs fallback
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
	if len(ds) == 0 {
		return drmDisplays(), nil // XWayland that reports no physical mm
	}
	return ds, nil
}

// Mode is one selectable video mode on an output: a resolution paired with one
// of the refresh rates xrandr lists beside it.
type Mode struct {
	W, H      int
	Rate      float64
	Current   bool // xrandr's '*' — the mode in force
	Preferred bool // xrandr's '+' — the sink's own preference, from EDID
}

// Output is a connector xrandr knows about, with every mode it offers.
// Deliberately NOT Display: that type exists to classify a display's physical
// size and drops any output without EDID millimetres, which is exactly the
// output a mis-negotiating TV presents. Mode control cares about connectors.
type Output struct {
	Name      string
	Connected bool
	Modes     []Mode
}

// reOutputLine matches the un-indented header that opens each output's block:
//
//	HDMI-1 connected primary 1920x1080+0+0 (normal ...) 528mm x 297mm
//	DP-1 disconnected (normal left inverted right x axis y axis)
var reOutputLine = regexp.MustCompile(`^(\S+) (connected|disconnected)\b`)

// reModeLine matches an indented mode line, capturing the resolution and the
// whole refresh-rate column that follows: "   1920x1080     60.00*+  50.00".
var reModeLine = regexp.MustCompile(`^\s+(\d+)x(\d+)\s+(\S.*?)\s*$`)

// Outputs enumerates every connector and the modes it offers. Unlike Displays
// this needs a real X server — there is no sysfs fallback, because setting a
// mode needs a display server regardless, so an output list nothing can act on
// would only be a lie.
func Outputs(ctx context.Context) ([]Output, error) {
	out, err := xrandrQuery(ctx)
	if err != nil {
		return nil, err
	}
	var outs []Output
	for _, line := range strings.Split(string(out), "\n") {
		if m := reOutputLine.FindStringSubmatch(line); m != nil {
			outs = append(outs, Output{Name: m[1], Connected: m[2] == "connected"})
			continue
		}
		if len(outs) == 0 {
			continue // the "Screen 0: ..." preamble
		}
		m := reModeLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		w, _ := strconv.Atoi(m[1])
		h, _ := strconv.Atoi(m[2])
		last := &outs[len(outs)-1]
		for _, mode := range parseRates(w, h, m[3]) {
			last.addMode(mode)
		}
	}
	return outs, nil
}

// parseRates turns one mode line's refresh-rate column into a Mode per rate.
// The '*' (current) and '+' (preferred) markers may be attached to the rate
// ("60.00*+") or stand alone ("60.00 +") depending on xrandr's column padding,
// so a bare marker token applies to the rate before it. A rate that doesn't
// parse (an interlaced "59.94i", say) is skipped rather than guessed at.
func parseRates(w, h int, rest string) []Mode {
	var ms []Mode
	for _, tok := range strings.Fields(rest) {
		num := strings.Trim(tok, "*+")
		if num == "" {
			if len(ms) > 0 {
				ms[len(ms)-1].Current = ms[len(ms)-1].Current || strings.Contains(tok, "*")
				ms[len(ms)-1].Preferred = ms[len(ms)-1].Preferred || strings.Contains(tok, "+")
			}
			continue
		}
		rate, err := strconv.ParseFloat(num, 64)
		if err != nil {
			continue
		}
		ms = append(ms, Mode{
			W: w, H: h, Rate: rate,
			Current:   strings.Contains(tok, "*"),
			Preferred: strings.Contains(tok, "+"),
		})
	}
	return ms
}

// sameMode is mode identity for this package: resolution plus refresh rate to
// the same 2dp the setting stores and xrandr --rate is given. Anything finer
// isn't addressable, so it isn't a difference.
func sameMode(a, b Mode) bool {
	return a.W == b.W && a.H == b.H && math.Abs(a.Rate-b.Rate) < 0.01
}

// addMode appends m unless the output already lists an addressable duplicate.
// Real hardware produces them: a laptop panel here advertises two distinct
// 1920x1080 modelines that both round to 60.05, and xrandr is addressed by
// resolution + rate — so they are one mode as far as anything here can say, and
// keeping both would put two identical rows in the picker with the same stored
// value. Markers are merged into the survivor rather than dropped with the
// copy, so "in use"/"recommended" doesn't depend on which came first.
func (o *Output) addMode(m Mode) {
	for i := range o.Modes {
		if sameMode(o.Modes[i], m) {
			o.Modes[i].Current = o.Modes[i].Current || m.Current
			o.Modes[i].Preferred = o.Modes[i].Preferred || m.Preferred
			return
		}
	}
	o.Modes = append(o.Modes, m)
}

// CurrentMode returns the mode an output is presently in. ok=false means
// xrandr marked none — the output isn't displaying, so there is nothing to
// restore, which is why a change to it is refused rather than made un-undoable.
func (o Output) CurrentMode() (Mode, bool) {
	for _, m := range o.Modes {
		if m.Current {
			return m, true
		}
	}
	return Mode{}, false
}

// FormatSetting renders an output and mode as the single string the
// display_mode setting stores and the picker offers as an option value:
// "HDMI-1 1920x1080 60.00". One value keeps the whole choice in one form
// field, which is what lets `hescli config set display_mode` set it too.
func FormatSetting(output string, m Mode) string {
	return fmt.Sprintf("%s %dx%d %.2f", output, m.W, m.H, m.Rate)
}

// ParseSetting splits a stored display_mode value back into an output name and
// a mode. ok=false on anything malformed — hescli writes this key with no
// validation at all, so a typo has to degrade to "no saved mode", never panic
// and never half-apply.
func ParseSetting(s string) (string, Mode, bool) {
	f := strings.Fields(s)
	if len(f) != 3 {
		return "", Mode{}, false
	}
	res := strings.SplitN(f[1], "x", 2)
	if len(res) != 2 {
		return "", Mode{}, false
	}
	w, err1 := strconv.Atoi(res[0])
	h, err2 := strconv.Atoi(res[1])
	rate, err3 := strconv.ParseFloat(f[2], 64)
	if err1 != nil || err2 != nil || err3 != nil || w <= 0 || h <= 0 || rate <= 0 {
		return "", Mode{}, false
	}
	return f[0], Mode{W: w, H: h, Rate: rate}, true
}

// Offers reports whether this output still lists the given mode. The guard on
// both the apply path and the boot-time re-apply: a saved mode that the sink no
// longer advertises (a different TV plugged in) must be skipped, not forced.
func (o Output) Offers(m Mode) bool {
	for _, have := range o.Modes {
		if sameMode(have, m) {
			return true
		}
	}
	return false
}

// drmRoot is the sysfs DRM directory — a variable so tests can point it at a
// fabricated tree (and so the xrandr-failure tests stay hermetic on dev boxes
// with real connectors).
var drmRoot = "/sys/class/drm"

// edidMagic is the fixed 8-byte EDID header.
var edidMagic = []byte{0x00, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0x00}

// drmDisplays is the Wayland/no-X fallback: enumerate connected (and, where
// the attribute is readable, enabled — a lid-closed laptop's panel is
// connected but disabled) connectors under drmRoot and take each physical
// size from EDID bytes 21/22 (cm; sysfs binary attrs stat as 0 bytes but read
// fully). Positions are unknowable without a display server, so the returned
// Displays carry zero geometry — ClassAt's containment test never matches
// them, its sole-display branch classifies the single-connector case (the
// HTPC-on-one-TV deployment this path exists for), and several connectors
// stay honestly unknown.
func drmDisplays() []Display {
	dirs, err := filepath.Glob(filepath.Join(drmRoot, "card*-*"))
	if err != nil {
		return nil
	}
	var ds []Display
	for _, dir := range dirs {
		status, err := os.ReadFile(filepath.Join(dir, "status"))
		if err != nil || strings.TrimSpace(string(status)) != "connected" {
			continue
		}
		if en, err := os.ReadFile(filepath.Join(dir, "enabled")); err == nil && strings.TrimSpace(string(en)) == "disabled" {
			continue
		}
		edid, err := os.ReadFile(filepath.Join(dir, "edid"))
		if err != nil || len(edid) < 23 || !bytes.Equal(edid[:8], edidMagic) {
			continue
		}
		mmw, mmh := int(edid[21])*10, int(edid[22])*10
		if mmw == 0 || mmh == 0 {
			continue // undefined size (projector) — no scale decision
		}
		ds = append(ds, Display{Name: filepath.Base(dir), MMW: mmw, MMH: mmh})
	}
	return ds
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
