package installer

import (
	"errors"

	"golang.org/x/sys/unix"
)

func renameNoReplace(parent int, source, target string) error {
	return unix.RenameatxNp(parent, source, parent, target, unix.RENAME_EXCL)
}

// noReplaceUnsupported reports that the volume lacks RENAME_EXCL, not a
// refused rename.
func noReplaceUnsupported(err error) bool {
	return errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EOPNOTSUPP)
}

func traversalFlags() int { return unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC }
