//go:build linux

// Package singleton lets a launch replace any already-running Hespera instance,
// so clicking the menu item kills the previous window's server and starts anew.
// This matters because the app binds a fixed loopback address — a second launch
// would otherwise collide on the port.
package singleton

import (
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ReplaceOthers sends SIGTERM to every other process running this same
// executable (matched by install path), leaving the current process. It returns
// the number signalled.
//
// Identity is the exe link path of /proc/<pid>/exe, not an EvalSymlinks-resolved
// inode: when the binary is replaced while the app is running — an upgrade or a
// reinstall, then a relaunch — an old instance's /proc/<pid>/exe becomes
// "/usr/bin/hespera (deleted)", which EvalSymlinks can't resolve (the path
// doesn't exist). Resolving would therefore error and silently skip every
// pre-upgrade instance, so they'd never be replaced and would pile up. Comparing
// the (deleted)-stripped link path instead treats every instance at the same
// install location as the same app, regardless of which inode it's running.
// (/proc/<pid>/exe is already the kernel-resolved real binary, so no symlink
// resolution is lost.)
func ReplaceOthers() int {
	self := procExePath("self")
	if self == "" {
		return 0
	}
	me := os.Getpid()

	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0
	}
	var signalled []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == me {
			continue
		}
		if procExePath(e.Name()) != self {
			continue
		}
		if syscall.Kill(pid, syscall.SIGTERM) == nil {
			signalled = append(signalled, pid)
		}
	}
	// Wait (bounded) for the signalled instances to actually exit before the
	// caller rebinds the shared, single-owner resources — the loopback port, the
	// management socket, and app.url. SIGTERM is async, so without this the old
	// and new instances overlap and race on those: notably the old instance's
	// socket-unlink-on-shutdown could remove the new instance's freshly-bound
	// socket (healthy server, dead hescli). Best-effort — mirror install.sh's
	// wait-then-proceed, then return even if one is wedged (the ownership-checked
	// unlink in management_linux.go is the backstop for that residual window).
	waitForExit(signalled, 5*time.Second)
	return len(signalled)
}

// waitForExit blocks until none of pids is alive or timeout elapses, whichever
// comes first. A no-op for an empty list.
func waitForExit(pids []int, timeout time.Duration) {
	if len(pids) == 0 {
		return
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !anyAlive(pids) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// anyAlive reports whether any of pids still exists. Signal 0 probes existence
// without affecting the process; these are not our children (a separate launch),
// so once one exits its own parent reaps it and Kill returns ESRCH.
func anyAlive(pids []int) bool {
	for _, pid := range pids {
		if syscall.Kill(pid, 0) == nil {
			return true
		}
	}
	return false
}

// procExePath reads /proc/<pid>/exe and returns its install path with the
// kernel's " (deleted)" suffix (present when the backing binary was unlinked)
// stripped. Returns "" on error, e.g. EACCES for another user's process.
func procExePath(pid string) string {
	target, err := os.Readlink("/proc/" + pid + "/exe")
	if err != nil {
		return ""
	}
	return cleanDeleted(target)
}

// cleanDeleted strips the kernel's trailing " (deleted)" marker from an exe link
// target, so a process whose binary has since been replaced still matches the
// live one by install path.
func cleanDeleted(target string) string {
	return strings.TrimSuffix(target, " (deleted)")
}
