package pathguard

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWithinRoot(t *testing.T) {
	tests := []struct {
		target string
		root   string
		want   bool
	}{
		{"/media/music", "/media", true},
		{"/media", "/media", true},
		{"/media/music/album", "/media", true},
		{"/etc/passwd", "/media", false},
		{"/mediaextra", "/media", false},
		{"", "/media", false},
		{"/media", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.target+"_under_"+tt.root, func(t *testing.T) {
			got := WithinRoot(tt.target, tt.root)
			if got != tt.want {
				t.Fatalf("WithinRoot(%q, %q) = %v, want %v", tt.target, tt.root, got, tt.want)
			}
		})
	}
}

func TestResolveExistingUnderRoot(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "child")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	resolved, err := ResolveExistingUnderRoot(root, sub)
	if err != nil {
		t.Fatalf("ResolveExistingUnderRoot: %v", err)
	}
	if resolved != sub {
		t.Fatalf("expected %q, got %q", sub, resolved)
	}
}

func TestResolveExistingUnderRoot_Outside(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()

	_, err := ResolveExistingUnderRoot(root, outside)
	if err == nil {
		t.Fatalf("expected error for path outside root")
	}
}

func TestResolveExistingUnderRoot_Symlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()

	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	_, err := ResolveExistingUnderRoot(root, link)
	if err == nil {
		t.Fatalf("expected error for symlink escaping root")
	}
}
