package installer

import "golang.org/x/sys/unix"

func publishDirectory(parent int, source, target string) error {
	return unix.Renameat2(parent, source, parent, target, unix.RENAME_NOREPLACE)
}

func traversalFlags() int { return unix.O_PATH | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC }
