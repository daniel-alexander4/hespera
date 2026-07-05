//go:build unix

package singleton

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
)

// dataDirLockName is the advisory-lock file inside the data dir. It exists only
// to hold an flock; nothing is written to it.
const dataDirLockName = "hespera.lock"

// ErrLocked means another live instance already holds the data-dir lock — a
// second instance must not start against the same data dir (duplicate job
// workers/watchers/ffmpeg fleets, contended SQLite writers). The caller refuses
// to start.
var ErrLocked = errors.New("data dir is locked by another running instance")

// AcquireDataDirLock takes an exclusive advisory lock on the data dir so only
// one Hespera instance runs against it at a time. The returned Closer releases
// the lock; the kernel also releases it automatically when the process exits
// (flock is tied to the open file description), so a crash never leaves a stale
// lock — the reason to prefer flock over a pidfile.
//
// Failure posture: a live holder (EWOULDBLOCK) returns ErrLocked → refuse to
// start. Any *other* error (the file can't be opened, or flock isn't supported
// on this filesystem) is best-effort — log and return a no-op closer with a nil
// error so the lock mechanism failing never bricks startup; the socket probe and
// launch guard remain as the softer backstops.
func AcquireDataDirLock(dataDir string) (io.Closer, error) {
	path := filepath.Join(dataDir, dataDirLockName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		slog.Warn("data dir lock unavailable — proceeding without it", "path", path, "err", err)
		return io.NopCloser(nil), nil
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		// flock not supported here (e.g. some network filesystems) — degrade to
		// best-effort rather than refuse a legitimate start.
		slog.Warn("data dir lock could not be taken — proceeding without it", "path", path, "err", err)
		return io.NopCloser(nil), nil
	}
	return &dataDirLock{f: f}, nil
}

type dataDirLock struct{ f *os.File }

func (l *dataDirLock) Close() error {
	// LOCK_UN is belt-and-braces: closing the fd already drops the lock. The lock
	// file is deliberately left in place (it's just an flock anchor; removing it
	// would race a concurrent acquire).
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	return l.f.Close()
}
