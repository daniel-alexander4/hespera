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
	killed := 0
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == me {
			continue
		}
		if procExePath(e.Name()) != self {
			continue
		}
		if syscall.Kill(pid, syscall.SIGTERM) == nil {
			killed++
		}
	}
	return killed
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
