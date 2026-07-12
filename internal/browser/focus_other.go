//go:build !linux

package browser

// FocusWindow is a no-op off Linux. macOS and Windows are click-to-focus and
// hand a newly launched app window the keyboard themselves; the
// focus-follows-mouse problem FocusWindow solves is an X11 WM policy
// (focus_linux.go).
func FocusWindow() {}
