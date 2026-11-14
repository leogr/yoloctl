package openshell

import (
	"errors"
	"os"
)

// launcherShell picks, in the image as the workload sees it, the first of
// LauncherShells whose program is an executable regular file (following
// links); none is an error, as upstream's exec without a shell.
func launcherShell(stat func(string) (os.FileInfo, error)) ([]string, error) {
	for _, shell := range LauncherShells {
		info, err := stat(shell[0])
		if err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			return shell, nil
		}
	}
	return nil, errors.New("the image provides no executable shell at /bin/bash, /usr/bin/bash or /bin/sh")
}
