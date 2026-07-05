//go:build !unix

package singleton

import (
	"errors"
	"io"
)

// ErrLocked is defined for cross-platform callers; the no-op AcquireDataDirLock
// below never returns it. The advisory data-dir lock relies on flock (a unix
// facility), so non-unix builds keep the prior behavior — app mode's random
// loopback port plus the attach guard already prevent the common double-launch
// there. A hard Windows lock (LockFileEx) would be a new dependency; deferred.
var ErrLocked = errors.New("data dir is locked by another running instance")

// AcquireDataDirLock is a no-op off unix — see ErrLocked. Returns a Closer that
// does nothing and a nil error so startup proceeds unchanged.
func AcquireDataDirLock(_ string) (io.Closer, error) {
	return io.NopCloser(nil), nil
}
