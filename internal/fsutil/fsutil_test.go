package fsutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "art.jpg")

	if err := WriteFileAtomic(path, []byte("first"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "first" {
		t.Fatalf("read back = %q, %v; want \"first\"", got, err)
	}

	// Overwrites cleanly.
	if err := WriteFileAtomic(path, []byte("second"), 0o644); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	got, _ = os.ReadFile(path)
	if string(got) != "second" {
		t.Fatalf("after overwrite = %q, want \"second\"", got)
	}

	// No temp leftovers in the dir.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Fatalf("leftover temp file: %s", e.Name())
		}
	}
}

func TestWriteReaderAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stream.bin")
	if err := WriteReaderAtomic(path, strings.NewReader("streamed"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "streamed" {
		t.Fatalf("read back = %q, want \"streamed\"", got)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("perm = %o, want 600", fi.Mode().Perm())
	}
}
