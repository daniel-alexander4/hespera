package main

import (
	"os"
	"path/filepath"
)

// writeFileAtomic writes data to path via a temp file in the same directory,
// fsync, chmod and an atomic rename, so a crash never leaves a truncated file
// at path and never replaces a good file with a half-written one. The parent
// directory must already exist.
//
// This is deliberately a hesplay-local copy of internal/fsutil.WriteFileAtomic
// rather than an import of it: cmd/hesplay/CLAUDE.md declares hesplay
// "stdlib-only" and no file in this package imports hespera/internal. What it
// is NOT is a third copy — saveNoiseConfig hand-rolled these same eighteen
// lines and now calls this instead, so the package has one atomic write.
//
// The fsyncs — of the file and of its directory — are the one difference from
// fsutil's version, and they are here because of what this package does on a
// damaged file: the passphrase reader treats an unusable file as a reason to
// move it aside and generate a new secret, so a power-cut mid-write would
// otherwise change the household's passphrase.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".hesplay-tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	cleanup := func(err error) error {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if _, err := f.Write(data); err != nil {
		return cleanup(err)
	}
	if err := f.Sync(); err != nil {
		return cleanup(err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, perm); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	// Fsync the DIRECTORY too, or the rename itself is not durable and a
	// power-cut can leave no file at all — which for the passphrase means the
	// next start generates a new one and the household is locked out. Syncing
	// the file's contents without syncing the directory entry is the classic
	// half-measure. Best-effort: some filesystems refuse to open a directory
	// for sync, and failing the whole write over that would be worse.
	if d, err := os.Open(filepath.Dir(path)); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
