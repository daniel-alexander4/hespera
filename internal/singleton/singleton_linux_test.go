//go:build linux

package singleton

import "testing"

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
