package operator

import "golang.org/x/sys/unix"

func renameLegacyIdentityNoReplace(fd int, from, to string) error {
	return unix.Renameat2(fd, from, fd, to, unix.RENAME_NOREPLACE)
}

func legacyIdentityAncestorAccess() int { return unix.O_PATH }
