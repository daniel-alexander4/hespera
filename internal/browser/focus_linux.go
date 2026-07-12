//go:build linux

package browser

// Focus-on-launch for X11 desktops running focus-follows-mouse.
//
// Under a click-to-focus WM the app window takes focus on its own and none of
// this runs to any effect. Under focus-follows-mouse (Cinnamon/Muffin
// focus-mode='mouse', and the sloppy variants) the WM *defines* focus as "the
// window under the pointer" and re-evaluates on pointer motion — so a window
// that maps on a display the pointer isn't on never gets the keyboard, and no
// amount of app-side focus-grabbing survives the next pointer twitch. The only
// move that is consistent with that policy rather than fighting it is to put the
// pointer where the window is.
//
// So: activate the window, then warp the pointer into it — but ONLY when the
// pointer is somewhere else. That gate matters. If the pointer already sits over
// the window (the single-display case, and every TV), warping would generate a
// mousemove the page can see, and couch.js sets html.using-mouse on any
// mousemove — which suppresses the remote's focus ring on load. Skipping the
// warp in that case keeps the couch experience intact, and confines the warp to
// the multi-monitor case where the user has a mouse in hand and using-mouse is
// the truth anyway.
//
// Best-effort throughout: needs xdotool (optional, probed — the ffmpeg/Chromium
// regime), needs X11, and every failure is a silent no-op. It can never keep the
// app from starting. Disable with HESPERA_NO_FOCUS_STEAL=1.

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	// The window is mapped by Chromium, not by us, so we poll for it.
	focusPollInterval = 200 * time.Millisecond
	focusPollTimeout  = 10 * time.Second
)

// rect is a window's absolute position and size on the X screen.
type rect struct{ X, Y, W, H int }

// contains reports whether the point (px, py) lies inside r. The decision that
// gates the pointer warp — pure, so it is unit-testable without an X server.
func (r rect) contains(px, py int) bool {
	return px >= r.X && px < r.X+r.W && py >= r.Y && py < r.Y+r.H
}

// FocusWindow gives the app window the keyboard, for X11 desktops whose WM
// won't do it (focus-follows-mouse, see the file comment). It blocks while it
// waits for the window to map, so callers run it in a goroutine; it is
// best-effort and reports nothing.
func FocusWindow() {
	if os.Getenv("HESPERA_NO_FOCUS_STEAL") != "" {
		return
	}
	// X11 only. xdotool's pointer/window calls are meaningless under a Wayland
	// compositor (which forbids focus stealing by design anyway).
	if os.Getenv("DISPLAY") == "" {
		return
	}
	xdotool, err := exec.LookPath("xdotool")
	if err != nil {
		slog.Debug("xdotool not installed — leaving window focus to the window manager")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), focusPollTimeout)
	defer cancel()

	win, err := waitForWindow(ctx, xdotool)
	if err != nil {
		slog.Debug("app window not found — not stealing focus", "err", err)
		return
	}

	geom, err := windowRect(ctx, xdotool, win)
	if err != nil {
		slog.Debug("could not read app window geometry", "err", err)
		return
	}
	px, py, err := pointerAt(ctx, xdotool)
	if err != nil {
		slog.Debug("could not read pointer location", "err", err)
		return
	}

	// Already over the window: the WM has (or will) hand it focus on its own,
	// and warping would only fire a mousemove that flips the UI into mouse
	// modality. Leave it alone.
	if geom.contains(px, py) {
		slog.Debug("pointer already over the app window — no focus steal needed")
		return
	}

	// Raise + request focus, then put the pointer in the middle of the window so
	// focus-follows-mouse keeps it there.
	_ = run(ctx, xdotool, "windowactivate", win)
	cx := strconv.Itoa(geom.X + geom.W/2)
	cy := strconv.Itoa(geom.Y + geom.H/2)
	if err := run(ctx, xdotool, "mousemove", cx, cy); err != nil {
		slog.Debug("could not warp the pointer to the app window", "err", err)
		return
	}
	slog.Debug("focused the app window", "window", win, "pointer", cx+","+cy)
}

// waitForWindow polls until Chromium has mapped a visible window with our
// WM_CLASS, and returns its id. Chromium can own several X windows; the last id
// is the most recently created, which is the one we just asked for.
func waitForWindow(ctx context.Context, xdotool string) (string, error) {
	var lastErr error
	for {
		out, err := output(ctx, xdotool, "search", "--onlyvisible", "--class", WMClass)
		if err == nil {
			if ids := strings.Fields(out); len(ids) > 0 {
				return ids[len(ids)-1], nil
			}
			lastErr = errNoWindow
		} else {
			lastErr = err // `search` exits non-zero when it matches nothing
		}
		select {
		case <-ctx.Done():
			return "", lastErr
		case <-time.After(focusPollInterval):
		}
	}
}

// errNoWindow is the sentinel for "xdotool ran fine but matched no window yet".
var errNoWindow = errNoWindowType{}

type errNoWindowType struct{}

func (errNoWindowType) Error() string { return "no window with WM_CLASS " + WMClass }

// windowRect reads a window's absolute geometry (`getwindowgeometry --shell`
// prints X=/Y=/WIDTH=/HEIGHT= lines).
func windowRect(ctx context.Context, xdotool, win string) (rect, error) {
	out, err := output(ctx, xdotool, "getwindowgeometry", "--shell", win)
	if err != nil {
		return rect{}, err
	}
	kv := shellVars(out)
	return rect{X: kv["X"], Y: kv["Y"], W: kv["WIDTH"], H: kv["HEIGHT"]}, nil
}

// pointerAt reads the pointer's absolute position (`getmouselocation --shell`
// prints X=/Y=/SCREEN=/WINDOW= lines).
func pointerAt(ctx context.Context, xdotool string) (int, int, error) {
	out, err := output(ctx, xdotool, "getmouselocation", "--shell")
	if err != nil {
		return 0, 0, err
	}
	kv := shellVars(out)
	return kv["X"], kv["Y"], nil
}

// shellVars parses xdotool's `--shell` output (KEY=int per line) into a map.
// Unparseable or absent keys read as 0 — callers only use keys xdotool always
// emits, and a zeroed geometry simply fails the contains() test harmlessly.
func shellVars(out string) map[string]int {
	kv := make(map[string]int, 6)
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		if n, err := strconv.Atoi(v); err == nil {
			kv[k] = n
		}
	}
	return kv
}

func run(ctx context.Context, name string, args ...string) error {
	return exec.CommandContext(ctx, name, args...).Run()
}

func output(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).Output()
	return string(out), err
}
