// Package fsutil holds small filesystem helpers shared across packages.
package fsutil

import (
	"io"
	"os"
	"path/filepath"
)

// WriteFileAtomic writes data to path via a temp file in the same directory and
// an atomic rename, so a crash or partial write never leaves a truncated file at
// path (and never replaces a good file with a half-written one). The parent
// directory must already exist.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	return writeAtomic(path, perm, func(f *os.File) error {
		_, err := f.Write(data)
		return err
	})
}

// WriteReaderAtomic is WriteFileAtomic for a streamed source (e.g. an HTTP
// response body) — it copies r into the temp file then renames, avoiding
// buffering the whole payload in memory. Bound r (io.LimitReader) at the call site.
func WriteReaderAtomic(path string, r io.Reader, perm os.FileMode) error {
	return writeAtomic(path, perm, func(f *os.File) error {
		_, err := io.Copy(f, r)
		return err
	})
}

func writeAtomic(path string, perm os.FileMode, fill func(*os.File) error) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if err := fill(f); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, perm); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
