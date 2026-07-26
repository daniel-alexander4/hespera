package main

import (
	"os"
	"syscall"
	"unsafe"
)

// Terminal setup for the transport keys (n / p / q while a track plays).
//
// Linux only, the priority_linux.go / focus_linux.go precedent: the termios
// ioctl numbers differ per unix, and every box this runs on is Linux. Elsewhere
// enterCbreak reports "no terminal control" and playback keeps today's shape,
// with stdin handed to the engine so mpv's own terminal keys still work.

// enterCbreak switches the terminal to cbreak: keystrokes arrive immediately
// (ICANON off) and aren't echoed back over the now-playing lines (ECHO off).
//
// Signal generation is deliberately LEFT ON (ISIG untouched) — full raw mode
// would swallow Ctrl+C, and hesplay's shutdown is built on SIGINT reaching
// signal.NotifyContext. Cbreak gets single keys without touching that.
//
// Returns a restore func, or nil when stdin is not a terminal (piped, a cron
// job, `hesplay … < /dev/null`) — a caller treats nil as "keys unavailable".
func enterCbreak() func() {
	fd := int(os.Stdin.Fd())
	var prev syscall.Termios
	if err := termiosGet(fd, &prev); err != nil {
		return nil
	}
	mode := prev
	mode.Lflag &^= syscall.ICANON | syscall.ECHO
	// Block until at least one byte is available, with no inter-byte timer: the
	// reader goroutine is meant to park in Read until a key is actually pressed.
	mode.Cc[syscall.VMIN] = 1
	mode.Cc[syscall.VTIME] = 0
	if err := termiosSet(fd, &mode); err != nil {
		return nil
	}
	return func() { _ = termiosSet(fd, &prev) }
}

func termiosGet(fd int, t *syscall.Termios) error { return termiosIoctl(fd, syscall.TCGETS, t) }
func termiosSet(fd int, t *syscall.Termios) error { return termiosIoctl(fd, syscall.TCSETS, t) }

func termiosIoctl(fd int, req uintptr, t *syscall.Termios) error {
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), req, uintptr(unsafe.Pointer(t))); errno != 0 {
		return errno
	}
	return nil
}
