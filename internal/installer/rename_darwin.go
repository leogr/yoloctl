package installer

import "golang.org/x/sys/unix"

func publishDirectory(parent int, source, target string) error {
	return unix.RenameatxNp(parent, source, parent, target, unix.RENAME_EXCL)
}

func traversalFlags() int { return unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC }
