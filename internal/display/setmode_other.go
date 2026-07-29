//go:build !linux

package display

// Runtime display-mode control is X11-only (see setmode_linux.go). On macOS and
// Windows the desktop owns display configuration and an app has no business
// changing it, so this is a no-op stub that reports itself unavailable — the
// settings card renders the reason and never offers the control.

import (
	"context"
	"errors"
)

// Available always reports unavailable off Linux.
func Available() (bool, string) {
	return false, "Changing the display mode is only available on Linux running X11. On this system the display settings belong to the desktop."
}

// SetMode is never reached — the callers gate on Available first.
func SetMode(ctx context.Context, output string, m Mode) error {
	return errors.New("changing the display mode is not supported on this platform")
}
