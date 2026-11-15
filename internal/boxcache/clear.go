package boxcache

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"syscall"
)

// Clear empties and recreates only the verified named cache leaf. The caller
// must hold normal box admission and prove that its container is not running.
// Removed reports the reduction in this directory's allocated-byte footprint,
// not a reservation or a promise about globally available filesystem space.
func Clear(ctx context.Context, name string, uid, gid int) (uint64, error) {
	return clear(ctx, Root, name, uid, gid)
}

// ClearWithDirectories restores the feature directories before measuring the
// final footprint, so reported freed bytes exclude their recreated allocation.
func ClearWithDirectories(ctx context.Context, name string, uid, gid int, dirs []string) (uint64, error) {
	return clear(ctx, Root, name, uid, gid, dirs...)
}

func clear(ctx context.Context, root, name string, uid, gid int, dirs ...string) (uint64, error) {
	if err := validateDirectoryPaths(dirs); err != nil {
		return 0, err
	}
	if _, err := Path(name); err != nil {
		return 0, err
	}
	if uid < 1 || gid < 1 {
		return 0, errors.New("cache leaf requires the positive space UID/GID")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	parent, err := openParent(root, false)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer func() { _ = parent.Close() }()
	if err := verifyLeaf(parent, name, uid, gid); errors.Is(err, os.ErrNotExist) {
		return 0, nil
	} else if err != nil {
		return 0, err
	}
	leaf, err := parent.OpenRoot(name)
	if err != nil {
		return 0, err
	}
	before, err := directoryBytes(ctx, leaf)
	if err == nil {
		err = clearChildren(ctx, leaf)
	}
	_ = leaf.Close()
	if err != nil {
		return 0, err
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := parent.Remove(name); err != nil {
		return 0, err
	}
	// Recreate the private leaf even if cancellation arrives after removal.
	// No later container is started when the enclosing operation is cancelled.
	if err := ensure(root, name, uid, gid); err != nil {
		return 0, err
	}
	if err := prepareDirectories(ctx, root, name, uid, gid, dirs); err != nil {
		return 0, err
	}
	leaf, err = parent.OpenRoot(name)
	if err != nil {
		return 0, err
	}
	defer func() { _ = leaf.Close() }()
	after, err := directoryBytes(ctx, leaf)
	if err != nil {
		return 0, err
	}
	if after > before {
		// Restoring newly declared empty directories can itself allocate blocks.
		if len(dirs) > 0 {
			return 0, nil
		}
		return 0, errors.New("cache usage grew while clearing; inspect it before further action")
	}
	return before - after, nil
}

func directoryBytes(ctx context.Context, root *os.Root) (uint64, error) {
	type inode struct{ dev, ino uint64 }
	seen := map[inode]bool{}
	var total uint64
	err := fs.WalkDir(root.FS(), ".", func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || st.Blocks < 0 {
			return errors.New("cache allocation is unavailable")
		}
		key := inode{uint64(st.Dev), st.Ino}
		if !seen[key] {
			total += uint64(st.Blocks) * 512
			seen[key] = true
		}
		return nil
	})
	return total, err
}

func clearChildren(ctx context.Context, root *os.Root) error {
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, readErr := dir.ReadDir(128)
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			name := entry.Name()
			if entry.IsDir() {
				child, err := root.OpenRoot(name)
				if err != nil {
					return err
				}
				err = clearChildren(ctx, child)
				_ = child.Close()
				if err != nil {
					return err
				}
			}
			// Remove unlinks a symlink itself, never its destination. A changed or
			// nonempty directory produces an error and retains the remaining cache.
			if err := root.Remove(name); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}
