//go:build unix

package main

import (
	"os"
	"syscall"
)

const noisePauseSupported = true

// signalNoisePause pauses or resumes the noise player. SoX exposes no IPC
// socket the way mpv does, so pause is the kernel's: SIGSTOP freezes the
// player mid-write and the ALSA buffer drains to silence within a moment;
// SIGCONT picks up exactly where it left off. On the hybrid path only the
// player is signalled — ffmpeg blocks on the full pipe within a second, so it
// pauses itself without being told, and resumes the same way.
func signalNoisePause(p *os.Process, pause bool) error {
	if pause {
		return p.Signal(syscall.SIGSTOP)
	}
	return p.Signal(syscall.SIGCONT)
}
