package boxexport

import "golang.org/x/sys/unix"

func renameExclusive(from int, old string, to int, name string) error {
	return unix.RenameatxNp(from, old, to, name, unix.RENAME_EXCL)
}

func pathDirectoryFlags() int {
	return unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
}
