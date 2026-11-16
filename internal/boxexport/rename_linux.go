package boxexport

import "golang.org/x/sys/unix"

func renameExclusive(from int, old string, to int, name string) error {
	return unix.Renameat2(from, old, to, name, unix.RENAME_NOREPLACE)
}

func pathDirectoryFlags() int {
	return unix.O_PATH | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
}
