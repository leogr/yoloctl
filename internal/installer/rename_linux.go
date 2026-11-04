package installer

import (
	"errors"

	"golang.org/x/sys/unix"
)

func renameNoReplace(parent int, source, target string) error {
	return unix.Renameat2(parent, source, parent, target, unix.RENAME_NOREPLACE)
}

// noReplaceUnsupported reports that the filesystem or kernel lacks
// RENAME_NOREPLACE (many FUSE mounts reply EINVAL), not a refused rename.
func noReplaceUnsupported(err error) bool {
	return errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EOPNOTSUPP)
}

func traversalFlags() int { return unix.O_PATH | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC }
