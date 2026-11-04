package lima

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"golang.org/x/sys/unix"
)

// Lima reserves OpenSSH's temporary control-socket suffix when checking its
// longest socket path. Keep this aligned with Lima's filenames.LongestSock:
// https://github.com/lima-vm/lima/blob/v2.2.1/pkg/limatype/filenames/filenames.go
const longestSocketName = "ssh.sock.1234567890123456"

// ValidateInstancePaths checks Lima's existing socket-path bound without
// creating directories or invoking limactl. Reads and cleanup do not use it.
func ValidateInstancePaths(space string) error {
	if spaceenv.Native() {
		return nil
	}
	home, err := layout.LimaHome()
	if err != nil {
		return err
	}
	// Match LimaDir: an absent home stays lexical, an existing home resolves
	// symlinks. In particular a short alias cannot hide a longer socket path.
	if _, err := os.Stat(home); !errors.Is(err, os.ErrNotExist) {
		home, err = filepath.EvalSymlinks(home)
		if err != nil {
			return fmt.Errorf("resolve Lima home: %w", err)
		}
	}
	return validateInstanceSocketPath(space, home, len(unix.RawSockaddrUnix{}.Path))
}

func validateInstanceSocketPath(space, home string, limit int) error {
	socket := filepath.Join(home, InstanceName(space), longestSocketName)
	if len(socket) >= limit {
		return fmt.Errorf("space %q: Lima socket path %q is %d bytes; must be less than %d bytes; choose a shorter space name or a shorter YOLOCTL_HOME for a new space", space, socket, len(socket), limit)
	}
	return nil
}
