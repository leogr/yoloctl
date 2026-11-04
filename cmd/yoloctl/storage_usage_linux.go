package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// diskUsageEntries bounds one directory measurement. The content store and
// BuildKit's state hold blobs and databases, not file trees.
const diskUsageEntries = 1 << 20

// diskUsage is the allocated size of a directory tree, each inode counted
// once, without following symlinks or leaving its filesystem. A missing
// directory holds nothing.
func diskUsage(ctx context.Context, root string) (uint64, error) {
	var top unix.Stat_t
	if err := unix.Lstat(root, &top); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return 0, nil
		}
		return 0, err
	}
	if top.Mode&unix.S_IFMT != unix.S_IFDIR {
		return 0, fmt.Errorf("%s is not a directory", root)
	}
	type inode struct{ dev, ino uint64 }
	seen := map[inode]bool{}
	var total uint64
	entries := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil // removed while walking
			}
			return err
		}
		if entries++; entries > diskUsageEntries {
			return fmt.Errorf("%s has more than %d entries", root, diskUsageEntries)
		}
		if entries%1024 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		var st unix.Stat_t
		if err := unix.Lstat(path, &st); err != nil {
			if errors.Is(err, unix.ENOENT) {
				return nil
			}
			return err
		}
		if uint64(st.Dev) != uint64(top.Dev) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		key := inode{uint64(st.Dev), st.Ino}
		if !seen[key] {
			seen[key] = true
			total += uint64(st.Blocks) * 512
		}
		return nil
	})
	return total, err
}
