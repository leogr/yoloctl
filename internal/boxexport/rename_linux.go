package boxexport

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func renameExclusive(from int, old string, to int, name string) error {
	err := unix.Renameat2(from, old, to, name, unix.RENAME_NOREPLACE)
	if err == unix.EINVAL {
		return fmt.Errorf("the destination may not support moving files into place without overwriting. Choose a local folder outside any SSHFS mount (%w)", err)
	}
	return err
}

func pathDirectoryFlags() int {
	return unix.O_PATH | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
}
