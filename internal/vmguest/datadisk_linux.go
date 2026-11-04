package vmguest

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/leogr/yoloctl/internal/ext4sb"
	"github.com/leogr/yoloctl/internal/vmwire"
)

const dataDiskDevice = "/dev/vdb"

// growTool runs one e2fsprogs command and returns its exit code; a command
// that cannot run at all is an error.
type growTool func(name string, args ...string) (int, error)

func runGrowTool(name string, args ...string) (int, error) {
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), nil
	}
	if err != nil {
		return -1, err
	}
	return 0, nil
}

// growDataDisk grows the data disk's filesystem offline, before it is
// mounted, from g.From to the g.To bytes the host extended the disk to. It
// requires exactly g's filesystem UUID, a disk of g.To bytes and a
// filesystem of g.From (or g.To, when an earlier boot finished resizing),
// repairs only what e2fsck -p repairs safely, resizes, and requires a clean
// read-only e2fsck and the new size afterwards. Every other outcome is an
// error and the disk stays unmounted.
func growDataDisk(device string, g vmwire.DataDiskGrow, run growTool) error {
	sb, size, err := deviceState(device)
	if err != nil {
		return err
	}
	switch {
	case sb.UUID != g.UUID:
		return fmt.Errorf("filesystem %s is not the space's data disk %s", sb.UUID, g.UUID)
	case size != g.To:
		return fmt.Errorf("disk is %d bytes, expected %d", size, g.To)
	case sb.Size != g.From && sb.Size != g.To:
		return fmt.Errorf("filesystem is %d bytes, expected %d", sb.Size, g.From)
	}
	// 0: clean; 1: errors corrected. Anything else needs a person.
	if code, err := run("/usr/sbin/e2fsck", "-f", "-p", device); err != nil || code > 1 {
		return errors.Join(fmt.Errorf("e2fsck -f -p exited %d before the grow", code), err)
	}
	if sb.Size == g.From {
		if code, err := run("/usr/sbin/resize2fs", device); err != nil || code != 0 {
			return errors.Join(fmt.Errorf("resize2fs exited %d", code), err)
		}
	}
	if code, err := run("/usr/sbin/e2fsck", "-f", "-n", device); err != nil || code != 0 {
		return errors.Join(fmt.Errorf("e2fsck -f -n exited %d after the grow", code), err)
	}
	if after, _, err := deviceState(device); err != nil {
		return err
	} else if after.UUID != g.UUID || after.Size != g.To {
		return fmt.Errorf("after the grow the filesystem is %s, %d bytes", after.UUID, after.Size)
	}
	fmt.Println(g.GrownLine())
	return nil
}

func deviceState(device string) (ext4sb.Superblock, uint64, error) {
	f, err := os.Open(device)
	if err != nil {
		return ext4sb.Superblock{}, 0, err
	}
	defer func() { _ = f.Close() }()
	size, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return ext4sb.Superblock{}, 0, err
	}
	sb, err := ext4sb.Read(f)
	return sb, uint64(size), err
}
