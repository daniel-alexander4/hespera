//go:build linux

package display

// Runtime display-mode control — X11 only.
//
// Reading a display's size (the rest of this package) degrades to sysfs when
// there's no X server. Changing a mode has no such fallback: it is an xrandr
// call against a live X display, so the whole feature is gated on being able to
// reach one.
//
// That gate is the load-bearing part on the deployment this exists for. A TV
// kiosk runs Hespera as a system service (hespera@.service, User=%i), which is
// session-less — systemd sets no DISPLAY, so xrandr cannot connect and every
// call here fails. The operator grants access by adding DISPLAY (and, if their
// X session keeps its cookie somewhere non-default, XAUTHORITY) to
// /etc/default/hespera. That is the same shape as the power button's polkit
// rule: documented, deliberate, and never shipped enabled. Available() reports
// exactly which link is missing so the setting can say so instead of offering a
// control that silently does nothing.
//
// Kill switch: HESPERA_NO_DISPLAY_CONTROL=1.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// modeSetTimeout bounds one xrandr call. A mode set that hasn't returned by
// now is wedged against the X server, not slow.
const modeSetTimeout = 15 * time.Second

// xrandrSet is the process seam, injectable for tests. CombinedOutput, not
// Output: xrandr explains a rejected mode on stderr, and that text is what the
// settings page shows the user.
var xrandrSet = func(ctx context.Context, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, "xrandr", args...).CombinedOutput()
}

// Available reports whether runtime mode control can work on this machine, and
// when it can't, a sentence saying why — rendered verbatim in Settings, so it
// is written for the person reading it, not for a log.
func Available() (bool, string) {
	if os.Getenv("HESPERA_NO_DISPLAY_CONTROL") != "" {
		return false, "Display control is switched off on this machine (HESPERA_NO_DISPLAY_CONTROL is set)."
	}
	if os.Getenv("DISPLAY") == "" {
		return false, "Hespera can't reach this machine's screen. It's running without a display session — add DISPLAY (for example DISPLAY=:0) to /etc/default/hespera and restart it."
	}
	if _, err := exec.LookPath("xrandr"); err != nil {
		return false, "xrandr isn't installed. On Debian and Ubuntu it comes from the x11-xserver-utils package."
	}
	return true, ""
}

// SetMode switches an output to the given mode. Thin by design: the caller
// gates on Available() (the settings page needs the reason string anyway), so
// this reports only what xrandr itself said went wrong.
func SetMode(ctx context.Context, output string, m Mode) error {
	ctx, cancel := context.WithTimeout(ctx, modeSetTimeout)
	defer cancel()
	out, err := xrandrSet(ctx,
		"--output", output,
		"--mode", fmt.Sprintf("%dx%d", m.W, m.H),
		"--rate", fmt.Sprintf("%.2f", m.Rate))
	if err != nil {
		if detail := strings.TrimSpace(string(out)); detail != "" {
			return fmt.Errorf("xrandr: %w: %s", err, detail)
		}
		return fmt.Errorf("xrandr: %w", err)
	}
	return nil
}
