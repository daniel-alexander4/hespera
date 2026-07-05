//go:build unix

package singleton

import (
	"errors"
	"testing"
)

// A second acquire on the same data dir must fail with ErrLocked while the first
// holds it, and succeed again once the first releases — the single-instance
// invariant the launch guard leans on.
func TestAcquireDataDirLockExcludesSecond(t *testing.T) {
	dir := t.TempDir()

	first, err := AcquireDataDirLock(dir)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	if _, err := AcquireDataDirLock(dir); !errors.Is(err, ErrLocked) {
		t.Fatalf("second acquire while held: got %v, want ErrLocked", err)
	}

	if err := first.Close(); err != nil {
		t.Fatalf("release: %v", err)
	}

	// Released — the lock is available again.
	second, err := AcquireDataDirLock(dir)
	if err != nil {
		t.Fatalf("re-acquire after release: %v", err)
	}
	_ = second.Close()
}

// Distinct data dirs never contend — the lock is per-dir.
func TestAcquireDataDirLockPerDir(t *testing.T) {
	a, err := AcquireDataDirLock(t.TempDir())
	if err != nil {
		t.Fatalf("acquire a: %v", err)
	}
	defer a.Close()
	b, err := AcquireDataDirLock(t.TempDir())
	if err != nil {
		t.Fatalf("acquire b (different dir must not contend): %v", err)
	}
	_ = b.Close()
}
