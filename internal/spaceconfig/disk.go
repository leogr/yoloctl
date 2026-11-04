package spaceconfig

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/leogr/yoloctl/internal/resources"
)

// The VM system disk: an explicit size is whole GiB of at least MinVMDisk;
// without one, space create sizes it from the host (AutoVMDisk). It holds
// images, container layers, BuildKit and each box's files, and is replaced
// by every space recreate, which is also when a changed size applies.
const (
	GiB          = uint64(1 << 30)
	MinVMDisk    = 20 * GiB
	AutoDiskMin  = 30 * GiB
	AutoDiskMax  = 50 * GiB
	maxVMDiskGiB = 1 << 20 // a parse bound, not a policy
)

// VMDiskBytes parses a saved or requested system disk size: whole GiB, at
// least MinVMDisk.
func VMDiskBytes(s string) (uint64, error) {
	n, err := resources.VMBytes(s)
	if err != nil {
		return 0, err
	}
	if n%GiB != 0 || n/GiB > maxVMDiskGiB {
		return 0, fmt.Errorf("system disk size %q must be whole GiB, such as 40GiB", s)
	}
	if n < MinVMDisk {
		return 0, fmt.Errorf("system disk size %q is below the %dGiB minimum", s, MinVMDisk/GiB)
	}
	return n, nil
}

// DiskSize formats a whole-GiB size as saved in the bootstrap.
func DiskSize(n uint64) string { return strconv.FormatUint(n/GiB, 10) + "GiB" }

// AutoVMDisk is the system disk size for a new space without an explicit
// size: half of the bytes available on the filesystem that will hold it,
// rounded down to whole GiB and clamped to AutoDiskMin–AutoDiskMax. The disk
// is sparse for data, so the floor applies even on a small host; the host
// storage guard, not the size, protects the host.
func AutoVMDisk(available uint64) uint64 {
	return min(max(available/2/GiB*GiB, AutoDiskMin), AutoDiskMax)
}

// The data disk holds accounts and declarations. It is never autosized and
// never reformatted after initialization; DataDiskGrow only grows it.
const (
	DefaultDataDisk = 10 * GiB
	MinDataDisk     = 4 * GiB
)

// DataDiskBytes parses a saved or requested data disk size: whole GiB, at
// least MinDataDisk; empty is the DefaultDataDisk of spaces created before
// the setting.
func DataDiskBytes(s string) (uint64, error) {
	if s == "" {
		return DefaultDataDisk, nil
	}
	n, err := resources.VMBytes(s)
	if err != nil {
		return 0, err
	}
	if n%GiB != 0 || n/GiB > maxVMDiskGiB {
		return 0, fmt.Errorf("data disk size %q must be whole GiB, such as 20GiB", s)
	}
	if n < MinDataDisk {
		return 0, fmt.Errorf("data disk size %q is below the %dGiB minimum", s, MinDataDisk/GiB)
	}
	return n, nil
}

// DataDiskGrow checkpoints an explicit data disk grow. Requested: the host
// is about to extend the disk file from From to To bytes. Extended: the file
// is To bytes, the filesystem still From until guest init grows and checks
// it before mounting at the next start; the host records completion, and
// drops the checkpoint, when a stopped VM's disk shows To. Backup names the
// pre-grow copy kept until then.
type DataDiskGrow struct {
	From   uint64 `yaml:"from" json:"from"`
	To     uint64 `yaml:"to" json:"to"`
	Phase  string `yaml:"phase" json:"phase"`
	Backup bool   `yaml:"backup,omitempty" json:"backup,omitempty"`
}

// Data disk grow phases.
const (
	GrowRequested = "requested"
	GrowExtended  = "extended"
)

func (g *DataDiskGrow) validate(b *Bootstrap, data uint64) error {
	switch {
	case g.Phase != GrowRequested && g.Phase != GrowExtended:
		return errors.New("invalid data disk grow phase")
	case b.Initializing || b.DiskUUID == "":
		return errors.New("a data disk grow needs an initialized data disk")
	case g.From < MinDataDisk || g.From%GiB != 0 || g.To <= g.From || g.To%GiB != 0 || g.To != data:
		return errors.New("invalid data disk grow sizes")
	}
	return nil
}
