package vmapi

import (
	"errors"

	"github.com/leogr/yoloctl/internal/ext4sb"
)

// ErrHostStoragePaused must never trigger a graceful-stop fallback to SIGKILL:
// the live VM retains dirty guest pages until host storage becomes available.
var ErrHostStoragePaused = errors.New("VM paused for host storage pressure")

// HostStorageStartFree is the host headroom a Firecracker VM needs to be
// created or started, before anything a create formats (see
// FormatAllocation). It is a threshold, not a reservation.
const HostStorageStartFree = uint64(4 << 30)

// FormatInodeRatio is the bytes per inode of the ext4 disks Firecracker
// formats: one inode per 16 KiB at every size, as container layers and
// build caches are inode-heavy.
const FormatInodeRatio = 16384

// FormatAllocation is the host space formatting a Firecracker disk of size
// bytes allocates at once: its zeroed inode tables (256 B per inode), 2% of
// those for group descriptors and bitmaps, and the 128 MiB journal. Data
// blocks stay sparse. It matches the measured 610 MiB, 1.09 GiB and 1.7 GiB
// for 30, 60 and 100 GiB disks within 2%. Lima formats inside the guest, so
// it allocates nothing on the host here.
func FormatAllocation(backend string, size uint64) uint64 {
	if backend != "firecracker" {
		return 0
	}
	inodes := size / FormatInodeRatio * 256
	return inodes + inodes/50 + 128<<20
}

// ErrNoReflink means the host filesystem cannot make a cheap copy of a disk
// (FICLONE): a data disk grow then needs an explicit choice to go without
// one. Nothing was changed.
var ErrNoReflink = errors.New("the host filesystem cannot make a cheap copy of the data disk")

// DataDiskGrower is implemented by adapters that can grow a data disk
// offline: the host extends the disk file while the VM is verified stopped,
// and guest init grows and checks the filesystem before mounting it. Every
// method refuses a VM that is not stopped with ErrNotStopped.
type DataDiskGrower interface {
	// DataDiskGrowSupported reports whether the image the space boots has
	// the grow in its init.
	DataDiskGrowSupported(space, image string) (bool, error)
	// DataDiskState reads the data disk's superblock and its file size.
	DataDiskState(space, id string) (ext4sb.Superblock, uint64, error)
	// ExtendDataDisk extends the disk file from from to to bytes, after a
	// reflink copy when backup is set (ErrNoReflink when impossible). An
	// already extended file is left alone.
	ExtendDataDisk(space, id string, from, to uint64, backup bool) error
	// DataDiskGrowLogged reports whether the VM's console shows init's
	// verified grow line for this grow.
	DataDiskGrowLogged(space, line string) (bool, error)
	// RemoveDataDiskBackup removes the pre-grow copy, if any.
	RemoveDataDiskBackup(id string) error
}
