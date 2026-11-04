package operator

import "golang.org/x/sys/unix"

func renameLegacyIdentityNoReplace(fd int, from, to string) error {
	return unix.RenameatxNp(fd, from, fd, to, unix.RENAME_EXCL)
}

func legacyIdentityAncestorAccess() int { return unix.O_RDONLY }
