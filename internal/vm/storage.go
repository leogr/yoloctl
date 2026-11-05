package vm

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/internal/vmapi"
	"golang.org/x/sys/unix"
)

// BackingAvailable measures the bytes available to this user on the host
// filesystem that will hold space's VM disks under backend kind: the
// directory above its instance directory (Firecracker's instances
// directory, Lima's home), or its nearest existing ancestor before the
// first space. It reports the measured path too. Nothing is created.
func BackingAvailable(kind, space string) (uint64, string, error) {
	d, err := Driver(kind)
	if err != nil {
		return 0, "", err
	}
	dir, err := d.InstanceDir(space)
	if err != nil {
		return 0, "", err
	}
	// Before new-space publication, capacity belongs to its admission home.
	// Once a bootstrap exists, InstanceDir above uses its recorded/legacy home.
	if d.Kind() == "lima" {
		if _, err := spaceconfig.LoadBootstrap(space); errors.Is(err, os.ErrNotExist) {
			home, err := layout.NewLimaHome()
			if err != nil {
				return 0, "", err
			}
			return backingAvailableAt(home)
		} else if err != nil && !errors.Is(err, spaceconfig.ErrLegacySchema) {
			return 0, "", err
		}
	}
	return backingAvailableAt(filepath.Dir(dir))
}

func backingAvailableAt(path string) (uint64, string, error) {
	original := path
	for {
		if _, err := os.Stat(path); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			return 0, "", err
		}
		parent := filepath.Dir(path)
		if parent == path {
			return 0, "", fmt.Errorf("no existing directory above %s", original)
		}
		path = parent
	}
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, "", fmt.Errorf("measure host filesystem at %s: %w", path, err)
	}
	bsize := uint64(st.Bsize)
	if bsize == 0 || uint64(st.Bavail) > ^uint64(0)/bsize {
		return 0, "", fmt.Errorf("invalid host filesystem capacity at %s", path)
	}
	return uint64(st.Bavail) * bsize, path, nil
}

// DataDiskGrower returns backend kind's offline data disk grow, or false
// when the adapter has none (Lima mounts the disk before yoloctl runs).
func DataDiskGrower(kind string) (vmapi.DataDiskGrower, bool) {
	d, err := Driver(kind)
	if err != nil {
		return nil, false
	}
	g, ok := d.(vmapi.DataDiskGrower)
	return g, ok
}
