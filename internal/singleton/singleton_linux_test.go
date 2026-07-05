//go:build linux

package singleton

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

// cleanDeleted must strip the kernel's " (deleted)" marker (the bug: an old
// instance whose binary was replaced has /proc/<pid>/exe = "<path> (deleted)",
// which the previous EvalSymlinks match errored on and skipped) while leaving a
// live path untouched, so both compare equal by install path.
func TestCleanDeleted(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"/usr/bin/hespera (deleted)", "/usr/bin/hespera"}, // replaced binary
		{"/usr/bin/hespera", "/usr/bin/hespera"},           // live binary, unchanged
		{"/tmp/build/hespera (deleted)", "/tmp/build/hespera"},
		{"", ""},
		{"/path/with (deleted) inside/hespera", "/path/with (deleted) inside/hespera"}, // only a trailing marker is stripped
	} {
		if got := cleanDeleted(c.in); got != c.want {
			t.Errorf("cleanDeleted(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// A live binary and the same binary after replacement must resolve to the same
// install path, so a relaunch's ReplaceOthers matches and signals the orphan.
func TestCleanDeletedMatchesAcrossReplace(t *testing.T) {
	live := cleanDeleted("/usr/bin/hespera")
	replaced := cleanDeleted("/usr/bin/hespera (deleted)")
	if live != replaced {
		t.Fatalf("live %q and replaced %q must match by install path", live, replaced)
	}
}

// anyAlive is the existence probe ReplaceOthers waits on. It must see this
// running process as alive and a reaped child as gone (ESRCH), and treat an
// empty list as nothing-alive.
func TestAnyAlive(t *testing.T) {
	if anyAlive(nil) {
		t.Fatal("empty pid list reported alive")
	}
	if !anyAlive([]int{os.Getpid()}) {
		t.Fatal("the running test process reported dead")
	}

	// A finished, reaped child no longer exists — Kill(pid, 0) returns ESRCH.
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil { // Run waits, so the child is reaped
		t.Fatalf("run true: %v", err)
	}
	if anyAlive([]int{cmd.Process.Pid}) {
		t.Fatal("a reaped child reported alive")
	}
}

// waitForExit returns immediately for an empty list and for already-dead pids
// (it must not spin until the timeout when nothing is alive).
func TestWaitForExitReturnsWhenGone(t *testing.T) {
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("run true: %v", err)
	}
	start := time.Now()
	waitForExit([]int{cmd.Process.Pid}, 5*time.Second)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("waitForExit lingered %v for an already-dead pid", elapsed)
	}
}
