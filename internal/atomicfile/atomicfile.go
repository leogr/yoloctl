// Package atomicfile replaces a file through a sibling temporary file and a
// rename, so readers never observe a partial write.
package atomicfile

import (
	"fmt"
	"os"
	"path/filepath"
)

// tempPattern is the os.CreateTemp pattern for path's sibling temporary file.
func tempPattern(path string) string {
	return "." + filepath.Base(path) + ".*"
}

// Write atomically replaces path with data at mode. The parent directory must
// exist.
//
// The temporary file is created 0600 by os.CreateTemp and set to mode through
// its descriptor BEFORE any content is written, so the content is never
// readable at a wider mode than the final file and no path is re-resolved for
// the chmod. It is flushed to disk before the rename and removed on failure.
// Its name (".<base>.<random>") never ends in the target's extension, so
// directory listings that select manifests by suffix ignore a leftover.
func Write(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), tempPattern(path))
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	renamed := false
	defer func() {
		if !renamed {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	renamed = true
	if err := SyncDir(filepath.Dir(path)); err != nil {
		return &CommitError{Err: err}
	}
	return nil
}

// CommitError means replacement succeeded but its directory could not be
// flushed. Callers must inspect the current value before retrying; the old
// value is not guaranteed to remain in place.
type CommitError struct{ Err error }

func (e *CommitError) Error() string {
	return fmt.Sprintf("file replaced; durability uncertain: %v", e.Err)
}
func (e *CommitError) Unwrap() error { return e.Err }

// SyncDir persists directory entries after rename, creation or deletion.
func SyncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return f.Sync()
}
