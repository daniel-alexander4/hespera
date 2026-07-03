package config

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
)

// managementSocketName is the hescli management socket's filename — the single
// definition; the server, its startup log, and the hescli client all derive
// the path through ManagementSocketPath.
const managementSocketName = "hescli.sock"

// sunPathSafeLen is a conservative bound on a unix socket path: Linux's
// sun_path is 108 bytes (including the NUL), BSD's 104. Staying at 100 leaves
// margin, so a path that passes here always binds.
const sunPathSafeLen = 100

// ManagementSocketPath returns the management socket path for a data dir:
// DataDir/hescli.sock when that fits sun_path, else a deterministic
// per-DataDir name under $XDG_RUNTIME_DIR (or the OS temp dir when unset) so
// an over-long DataDir degrades to a working socket instead of a failed bind.
// A pure function of (dataDir, env): the server and hescli both call it, so
// they always agree without coordination — and the hashed name keeps distinct
// DataDirs (a real install vs. a sandbox) from colliding on the shared
// runtime dir. The peer-cred accept gate enforces authorization regardless of
// which directory the socket lands in.
func ManagementSocketPath(dataDir string) string {
	preferred := filepath.Join(dataDir, managementSocketName)
	if len(preferred) <= sunPathSafeLen {
		return preferred
	}
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = os.TempDir()
	}
	sum := sha256.Sum256([]byte(dataDir))
	return filepath.Join(dir, fmt.Sprintf("hespera-%x.sock", sum[:6]))
}
