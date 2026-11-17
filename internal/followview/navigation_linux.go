package followview

import "golang.org/x/sys/unix"

func navigationDirectoryFlags() int {
	return unix.O_PATH | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
}
