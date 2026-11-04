//go:build linux && amd64

package firecracker

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/leogr/yoloctl/internal/ext4sb"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/internal/vmapi"
	"github.com/leogr/yoloctl/internal/vmwire"
	"golang.org/x/sys/unix"
)

var _ vmapi.DataDiskGrower = Driver{}

const (
	dataDiskFile   = "data.ext4"
	dataDiskBackup = "data.ext4.pre-grow"
	// logTail bounds how much of vm.log is searched for init's grow line.
	logTail = 4 << 20
)

// stopped holds the instance lock of space while fn runs, so no helper can
// start the VM in between, and refuses a VM that is running or of
// uncertain state. A space without an instance directory is stopped.
func (d Driver) stopped(space string, fn func() error) error {
	dir, e := d.InstanceDir(space)
	if e != nil {
		return e
	}
	if _, e = os.Lstat(dir); errors.Is(e, os.ErrNotExist) {
		return fn()
	} else if e != nil {
		return e
	}
	lock, e := lockFile(filepath.Join(dir, "lock"))
	if errors.Is(e, unix.EWOULDBLOCK) {
		return fmt.Errorf("%w: %v", vmapi.ErrNotStopped, e)
	}
	if e != nil {
		return e
	}
	defer func() { _ = lock.Close() }()
	live, e := running(dir)
	if e != nil {
		return fmt.Errorf("%w: %v", vmapi.ErrNotStopped, e)
	}
	if live {
		return vmapi.ErrNotStopped
	}
	return fn()
}

func (d Driver) DataDiskGrowSupported(space, image string) (bool, error) {
	dir, e := d.InstanceDir(space)
	if e != nil {
		return false, e
	}
	// The instance's own pinned image is what boots; without an instance,
	// the space's selected image.
	if c, e := ReadConfig(dir); e == nil {
		return c.Image.DataDiskGrow >= DataDiskGrowVersion, nil
	} else if !errors.Is(e, os.ErrNotExist) {
		return false, e
	}
	m, e := ReadImage(image)
	if e != nil {
		return false, e
	}
	return m.DataDiskGrow >= DataDiskGrowVersion, nil
}

// openDataDisk opens the data disk file without following links and requires
// a singly linked regular file.
func (d Driver) openDataDisk(id string, flag int) (*os.File, string, error) {
	dir, e := d.AccountDiskDir(id)
	if e != nil {
		return nil, "", e
	}
	path := filepath.Join(dir, dataDiskFile)
	fd, e := unix.Open(path, flag|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if e != nil {
		return nil, "", fmt.Errorf("open data disk: %w", e)
	}
	f := os.NewFile(uintptr(fd), path)
	var st unix.Stat_t
	if e = unix.Fstat(fd, &st); e != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Nlink != 1 {
		_ = f.Close()
		return nil, "", errors.Join(errors.New("data disk must be a singly linked regular file"), e)
	}
	return f, dir, nil
}

func (d Driver) DataDiskState(space, id string) (sb ext4sb.Superblock, size uint64, err error) {
	err = d.stopped(space, func() error {
		f, _, e := d.openDataDisk(id, unix.O_RDONLY)
		if e != nil {
			return e
		}
		defer func() { _ = f.Close() }()
		st, e := f.Stat()
		if e != nil {
			return e
		}
		size = uint64(st.Size())
		sb, e = ext4sb.Read(f)
		return e
	})
	return sb, size, err
}

func (d Driver) ExtendDataDisk(space, id string, from, to uint64, backup bool) error {
	if from == 0 || to <= from || to > 1<<50 {
		return fmt.Errorf("invalid data disk grow %d → %d", from, to)
	}
	return d.stopped(space, func() error {
		f, dir, e := d.openDataDisk(id, unix.O_RDWR)
		if e != nil {
			return e
		}
		defer func() { _ = f.Close() }()
		st, e := f.Stat()
		if e != nil {
			return e
		}
		switch uint64(st.Size()) {
		case to:
			return nil // extended by an interrupted earlier attempt
		case from:
		default:
			return fmt.Errorf("data disk file is %d bytes, expected %d", st.Size(), from)
		}
		// Extending allocates nothing; the start headroom still applies.
		if e = requireHostStorage(dir); e != nil {
			return e
		}
		if backup {
			if e = cloneDataDisk(f, dir, from); e != nil {
				return e
			}
		}
		if e = f.Truncate(int64(to)); e != nil {
			return e
		}
		if e = f.Sync(); e != nil {
			return e
		}
		return syncDir(dir)
	})
}

// cloneDataDisk makes the pre-grow copy with FICLONE, which shares blocks
// and costs no space or time; a copy left by an interrupted earlier attempt
// of the same size is kept.
func cloneDataDisk(src *os.File, dir string, size uint64) error {
	path := filepath.Join(dir, dataDiskBackup)
	fd, e := unix.Open(path, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if errors.Is(e, unix.EEXIST) {
		var st unix.Stat_t
		if e = unix.Lstat(path, &st); e != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || uint64(st.Size) != size {
			return errors.Join(fmt.Errorf("an unexpected %s exists; inspect it before retrying", path), e)
		}
		return nil
	}
	if e != nil {
		return e
	}
	dst := os.NewFile(uintptr(fd), path)
	e = unix.IoctlFileClone(fd, int(src.Fd()))
	if e == nil {
		e = dst.Sync()
	}
	e = errors.Join(e, dst.Close())
	if e != nil {
		_ = os.Remove(path)
		if errors.Is(e, unix.EOPNOTSUPP) || errors.Is(e, unix.EXDEV) || errors.Is(e, unix.EINVAL) || errors.Is(e, unix.ENOTTY) {
			return fmt.Errorf("%w: %v", vmapi.ErrNoReflink, e)
		}
		return e
	}
	return syncDir(dir)
}

func (d Driver) DataDiskGrowLogged(space, line string) (bool, error) {
	log, e := d.LogPath(space)
	if e != nil {
		return false, e
	}
	f, e := os.Open(log)
	if errors.Is(e, os.ErrNotExist) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	defer func() { _ = f.Close() }()
	st, e := f.Stat()
	if e != nil {
		return false, e
	}
	off := max(st.Size()-logTail, 0)
	tail, e := io.ReadAll(io.NewSectionReader(f, off, st.Size()-off))
	if e != nil {
		return false, e
	}
	return bytes.Contains(tail, []byte(line)), nil
}

func (d Driver) RemoveDataDiskBackup(id string) error {
	dir, e := d.AccountDiskDir(id)
	if e != nil {
		return e
	}
	if e = os.Remove(filepath.Join(dir, dataDiskBackup)); e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
	return syncDir(dir)
}

// pendingDataDiskGrow is the grow init must perform at this boot: the
// space's extended grow, for the data disk this instance attaches.
func pendingDataDiskGrow(c Config) (*vmwire.DataDiskGrow, error) {
	boot, e := spaceconfig.LoadBootstrap(c.Name)
	if e != nil {
		// Booting without the grow is safe: the filesystem stays its old
		// size in the larger disk, and the grow waits for a later start.
		fmt.Fprintln(os.Stderr, "data disk grow not requested: read bootstrap:", e)
		return nil, nil
	}
	g := boot.DataDiskGrow
	if g == nil || g.Phase != spaceconfig.GrowExtended {
		return nil, nil
	}
	if boot.DiskID != c.Spec.DiskID || boot.DiskUUID == "" || boot.Initializing {
		return nil, errors.New("data disk grow names another or an uninitialized data disk")
	}
	return &vmwire.DataDiskGrow{UUID: boot.DiskUUID, From: g.From, To: g.To}, nil
}
