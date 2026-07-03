package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestManagementSocketPathShortDataDir(t *testing.T) {
	got := ManagementSocketPath("/var/lib/hespera")
	if got != filepath.Join("/var/lib/hespera", "hescli.sock") {
		t.Fatalf("short DataDir should keep the in-dir socket, got %q", got)
	}
}

func TestManagementSocketPathLongDataDirFallsBack(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
	long := "/" + strings.Repeat("a", sunPathSafeLen) // preferred path can't fit
	got := ManagementSocketPath(long)
	if filepath.Dir(got) != "/run/user/1000" {
		t.Fatalf("long DataDir should fall back to XDG_RUNTIME_DIR, got %q", got)
	}
	base := filepath.Base(got)
	if !strings.HasPrefix(base, "hespera-") || !strings.HasSuffix(base, ".sock") {
		t.Fatalf("fallback name should be hespera-<hash>.sock, got %q", base)
	}
	if len(got) > sunPathSafeLen {
		t.Fatalf("fallback path should itself fit sun_path, got %d bytes", len(got))
	}

	// Deterministic: the server and hescli must independently derive the same
	// path from the same DataDir.
	if again := ManagementSocketPath(long); again != got {
		t.Fatalf("not deterministic: %q vs %q", got, again)
	}

	// Distinct DataDirs must not collide on the shared runtime dir.
	other := "/" + strings.Repeat("b", sunPathSafeLen)
	if ManagementSocketPath(other) == got {
		t.Fatal("distinct DataDirs collided on one fallback socket")
	}
}

func TestManagementSocketPathFallbackWithoutRuntimeDir(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")
	long := "/" + strings.Repeat("c", sunPathSafeLen)
	got := ManagementSocketPath(long)
	if strings.HasPrefix(got, long) {
		t.Fatalf("should not stay under the over-long DataDir, got %q", got)
	}
	if filepath.Base(got) == "hescli.sock" {
		t.Fatalf("fallback should use the hashed per-DataDir name, got %q", got)
	}
}
