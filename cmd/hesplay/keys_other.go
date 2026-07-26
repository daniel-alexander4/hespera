//go:build !linux

package main

// enterCbreak is a no-op off Linux: the termios ioctl numbers differ per unix
// and there is nothing equivalent on Windows, so those builds simply don't get
// the transport keys. Playback is unchanged there — stdin stays with the engine,
// so mpv's own terminal keybindings still work.
func enterCbreak() func() { return nil }
