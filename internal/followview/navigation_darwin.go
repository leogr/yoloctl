package followview

import "golang.org/x/sys/unix"

func navigationDirectoryFlags() int {
	return unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
}
