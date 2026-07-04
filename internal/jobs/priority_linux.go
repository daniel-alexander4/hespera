//go:build linux

package jobs

import (
	"runtime"
	"syscall"
)

// ioprio_set(2) constants (linux/ioprio.h) — stdlib syscall has the syscall
// number per arch but not the class encoding.
const (
	ioprioClassShift = 13
	ioprioClassIdle  = 3 // IOPRIO_CLASS_IDLE: disk time only when no one else wants it
	ioprioWhoProcess = 1 // with pid 0 → the calling thread
)

// lowerWorkerPriority pins the calling goroutine (the single job worker) to its
// OS thread and drops that thread to idle I/O class + nice 19. Both properties
// are per-thread and inherited across fork/exec, so every ffmpeg/ffprobe a job
// spawns — and every in-process full-file read (the music scanner's SHA-256
// pass) — runs at background priority, while request-time work on other threads
// (playback streaming, photo renditions) keeps normal priority. The thread stays
// locked for the worker's lifetime; both calls are best-effort (lowering one's
// own priority needs no privilege; the idle class means full disk speed when the
// disk is otherwise idle, yielding only under contention — note that only some
// I/O schedulers, e.g. bfq, honor I/O priorities).
func lowerWorkerPriority() {
	runtime.LockOSThread()
	_, _, _ = syscall.Syscall(syscall.SYS_IOPRIO_SET, ioprioWhoProcess, 0, ioprioClassIdle<<ioprioClassShift)
	_ = syscall.Setpriority(syscall.PRIO_PROCESS, 0, 19)
}
