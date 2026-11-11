package main

import (
	"errors"
	"fmt"
	"io"

	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	vmhost "github.com/leogr/yoloctl/internal/vm"
	"github.com/leogr/yoloctl/internal/vmapi"
	"github.com/leogr/yoloctl/internal/vmwire"
	"github.com/spf13/cobra"
)

// measureSpaceBacking is replaceable so tests can model a host's free space.
var measureSpaceBacking = vmhost.BackingAvailable

const diskFlagHelp = "VM system disk: auto (half of the host filesystem's free space, 30–50 GiB) or an explicit size of at least 20GiB, such as 60GiB; it holds images, containers and box files"

// selectSpaceDisk chooses a new space's system disk size once, before
// anything is written: --disk, or auto. It refuses when the host cannot
// format the disk above the start headroom, and says when the host is too
// small for the VM to fill it.
func selectSpaceDisk(cmd *cobra.Command, backend, space string) (string, error) {
	value, _ := cmd.Flags().GetString("disk")
	available, path, err := measureSpaceBacking(backend, space)
	if err != nil {
		return "", err
	}
	return resolveSpaceDisk(value, backend, available, path, cmd.ErrOrStderr())
}

func resolveSpaceDisk(value, backend string, available uint64, path string, errw io.Writer) (string, error) {
	gib := func(n uint64) string { return fmt.Sprintf("%.1f GiB", float64(n)/float64(spaceconfig.GiB)) }
	var size uint64
	if value == "" || value == "auto" {
		size = spaceconfig.AutoVMDisk(available)
		cliInfo(logging.Host, "system disk %d GiB (half of %d GiB free)", size/spaceconfig.GiB, available/spaceconfig.GiB)
	} else {
		n, err := spaceconfig.VMDiskBytes(value)
		if err != nil {
			return "", asUserError(err)
		}
		size = n
	}
	if backend == "firecracker" {
		if need := vmapi.HostStorageStartFree + vmapi.FormatAllocation(backend, size); available < need {
			return "", asUserError(fmt.Errorf("cannot create space: %s free on %s, at least %s is needed to format a %d GiB system disk; free space or choose another config root",
				gib(available), path, gib(need), size/spaceconfig.GiB))
		}
	}
	diskCapacityNotice(errw, backend, size, available)
	return spaceconfig.DiskSize(size), nil
}

// diskCapacityNotice says when the host has less free space than the disk:
// the disk is sparse, so this is allowed, but the VM cannot fill it.
func diskCapacityNotice(errw io.Writer, backend string, size, available uint64) {
	if size <= available {
		return
	}
	pause := ""
	if backend == "firecracker" {
		pause = ", and the host pauses the VM at 2 GiB free"
	}
	cliWarn(logging.Host, "the host filesystem has %.1f GiB free; the VM's %d GiB disk cannot be filled%s", float64(available)/float64(spaceconfig.GiB), size/spaceconfig.GiB, pause)
}

// growSpaceDisk validates a requested system disk size for a saved space:
// whole GiB above the minimum and never smaller than the saved size, since
// the setting only grows. It returns the size to save (the saved one when
// unchanged) and says when the host has less free space than the disk.
func growSpaceDisk(boot *spaceconfig.Bootstrap, requested string, errw io.Writer) (string, error) {
	if requested == "" || requested == boot.VM.Disk {
		return boot.VM.Disk, nil
	}
	size, err := spaceconfig.VMDiskBytes(requested)
	if err != nil {
		return "", asUserError(err)
	}
	saved, err := spaceconfig.VMDiskBytes(boot.VM.Disk)
	if err != nil {
		return "", err
	}
	switch {
	case size < saved:
		return "", asUserError(fmt.Errorf("the system disk only grows: %s is smaller than the saved %s", requested, boot.VM.Disk))
	case size == saved:
		return boot.VM.Disk, nil
	}
	if available, _, err := measureSpaceBacking(boot.VM.Backend, boot.Name); err == nil {
		diskCapacityNotice(errw, boot.VM.Backend, size, available)
	}
	return spaceconfig.DiskSize(size), nil
}

// diskLabel shows a saved whole-GiB size the way notices do ("50 GiB").
func diskLabel(saved string) string {
	if n, err := spaceconfig.VMDiskBytes(saved); err == nil {
		return fmt.Sprintf("%d GiB", n/spaceconfig.GiB)
	}
	return saved
}

func savedDiskBytes(saved string) uint64 {
	n, _ := spaceconfig.VMDiskBytes(saved)
	return n
}

// pendingDiskNote names a saved system disk size the VM does not have yet;
// it is empty when they agree or either is unknown.
func pendingDiskNote(current uint64, saved string) string {
	n := savedDiskBytes(saved)
	if n == 0 || current == 0 || n == current {
		return ""
	}
	return fmt.Sprintf("disk %s saved, applies at the next space recreate", saved)
}

// diskShown is space show's disk line: the VM's disk, then a saved size it
// does not have yet; without a VM, the saved size.
func diskShown(current uint64, saved string) string {
	if current == 0 && savedDiskBytes(saved) != 0 {
		return saved + " (saved)"
	}
	if note := pendingDiskNote(current, saved); note != "" {
		return humanBytes(current) + " (" + note + ")"
	}
	return humanBytes(current)
}

func diskBytesLabel(n uint64) string { return fmt.Sprintf("%d GiB", n/spaceconfig.GiB) }

const dataDiskFlagHelp = "Data disk for accounts and declarations, in whole GiB of at least 4GiB (default 10GiB); it is never reformatted and only grows"

// Replaceable in tests.
var dataDiskGrower = vmhost.DataDiskGrower

// selectSpaceDataDisk validates --data-disk for a new space.
func selectSpaceDataDisk(cmd *cobra.Command) (string, error) {
	value, _ := cmd.Flags().GetString("data-disk")
	n, err := spaceconfig.DataDiskBytes(value)
	if err != nil {
		return "", asUserError(err)
	}
	return spaceconfig.DiskSize(n), nil
}

// growSpaceDataDisk grows a stopped space's data disk to requested: the
// host checks the disk is the space's (UUID) at its saved size, records a
// requested checkpoint, makes a reflink copy unless noBackup, extends the
// file and records it extended. Guest init grows and checks the filesystem
// before mounting it at the next start; settleDataDiskGrow records the end.
// It refuses, changing nothing, when the adapter cannot grow offline, the
// image predates the grow, the disk is not as recorded, or no cheap copy is
// possible without noBackup.
func growSpaceDataDisk(boot *spaceconfig.Bootstrap, requested string, noBackup bool, out io.Writer) error {
	from, err := spaceconfig.DataDiskBytes(boot.VM.DataDisk)
	if err != nil {
		return err
	}
	if requested == "" {
		return nil
	}
	to, err := spaceconfig.DataDiskBytes(requested)
	if err != nil {
		return asUserError(err)
	}
	switch {
	case to == from && boot.DataDiskGrow == nil:
		return nil
	case boot.DataDiskGrow != nil && (boot.DataDiskGrow.Phase != spaceconfig.GrowRequested || boot.DataDiskGrow.To != to):
		g := boot.DataDiskGrow
		return asUserError(fmt.Errorf("a data disk grow %s → %s is pending; start the space to finish it first", diskBytesLabel(g.From), diskBytesLabel(g.To)))
	case boot.DataDiskGrow != nil:
		from = boot.DataDiskGrow.From // resume an interrupted request
	case to < from:
		return asUserError(fmt.Errorf("the data disk only grows: %s is smaller than the saved %s", requested, diskBytesLabel(from)))
	}
	g, ok := dataDiskGrower(boot.VM.Backend)
	if !ok {
		return asUserError(errors.New("growing the data disk is not supported on Lima yet: Lima mounts it before yoloctl runs, so there is no offline step to grow and check it safely"))
	}
	if boot.Initializing || boot.DiskUUID == "" {
		return asUserError(errors.New("finish data disk initialization (space start) before growing it"))
	}
	if supported, err := g.DataDiskGrowSupported(boot.Name, boot.VM.Image); err != nil {
		return err
	} else if !supported {
		return asUserError(fmt.Errorf("this space's VM image predates the data disk grow; run yoloctl space recreate %s --update-vm-image first", boot.Name))
	}
	sb, size, err := g.DataDiskState(boot.Name, boot.DiskID)
	if err != nil {
		return err
	}
	if sb.UUID != boot.DiskUUID || sb.Size != from || (size != from && size != to) {
		return fmt.Errorf("the data disk is not as recorded (filesystem %s of %d bytes in a %d byte file; expected %s of %d bytes); nothing was changed", sb.UUID, sb.Size, size, boot.DiskUUID, from)
	}
	previous := boot.VM.DataDisk
	boot.DataDiskGrow = &spaceconfig.DataDiskGrow{From: from, To: to, Phase: spaceconfig.GrowRequested, Backup: !noBackup}
	boot.VM.DataDisk = spaceconfig.DiskSize(to)
	if err := boot.Save(); err != nil {
		return err
	}
	if err := g.ExtendDataDisk(boot.Name, boot.DiskID, from, to, !noBackup); err != nil {
		if errors.Is(err, vmapi.ErrNoReflink) {
			boot.DataDiskGrow, boot.VM.DataDisk = nil, previous
			if serr := boot.Save(); serr != nil {
				return errors.Join(err, serr)
			}
			return asUserError(fmt.Errorf("%w; nothing was changed. Run yoloctl space export %s ARCHIVE first, then repeat with --no-backup", err, boot.Name))
		}
		return fmt.Errorf("data disk grow requested but the disk was not extended; inspect, then repeat space set --data-disk %s: %w", requested, err)
	}
	boot.DataDiskGrow.Phase = spaceconfig.GrowExtended
	if err := boot.Save(); err != nil {
		return err
	}
	cliInfo(logging.Host, "space %s: data disk %s → %s extended; its filesystem is grown and checked before mounting at the next yoloctl space start %s", boot.Name, diskBytesLabel(from), diskBytesLabel(to), boot.Name)
	return nil
}

// settleDataDiskGrow records a finished data disk grow once the VM is
// stopped (the adapter verifies it under the instance lock): the disk's
// filesystem is the space's at the new size and the last boot logged init's
// clean check. It then drops the checkpoint and the pre-grow copy.
func settleDataDiskGrow(boot *spaceconfig.Bootstrap, out io.Writer) error {
	grow := boot.DataDiskGrow
	if grow == nil || grow.Phase != spaceconfig.GrowExtended {
		return nil
	}
	g, ok := dataDiskGrower(boot.VM.Backend)
	if !ok {
		return nil
	}
	sb, _, err := g.DataDiskState(boot.Name, boot.DiskID)
	if err != nil || sb.UUID != boot.DiskUUID || sb.Size != grow.To {
		return err
	}
	line := vmwire.DataDiskGrow{UUID: boot.DiskUUID, From: grow.From, To: grow.To}.GrownLine()
	if logged, err := g.DataDiskGrowLogged(boot.Name, line); err != nil || !logged {
		return err
	}
	if grow.Backup {
		if err := g.RemoveDataDiskBackup(boot.DiskID); err != nil {
			return err
		}
	}
	boot.DataDiskGrow = nil
	if err := boot.Save(); err != nil {
		return err
	}
	cliInfo(logging.Host, "space %s: data disk %s → %s, verified", boot.Name, diskBytesLabel(grow.From), diskBytesLabel(grow.To))
	return nil
}

// dataDiskShown is space show's data disk line.
func dataDiskShown(boot *spaceconfig.Bootstrap, growSupported bool) string {
	size, _ := spaceconfig.DataDiskBytes(boot.VM.DataDisk)
	line := diskBytesLabel(size)
	switch g := boot.DataDiskGrow; {
	case g != nil && g.Phase == spaceconfig.GrowRequested:
		line += fmt.Sprintf(" (grow from %s requested, not extended; repeat space set --data-disk)", diskBytesLabel(g.From))
	case g != nil:
		line += fmt.Sprintf(" (grow from %s pending: done at the next start, recorded at the next stop)", diskBytesLabel(g.From))
	case boot.VM.Backend == "firecracker" && !growSupported:
		line += " (a data disk grow needs a VM image update)"
	}
	return line
}

// dataDiskSetting is the saved data disk size as set and edit show it.
func dataDiskSetting(boot *spaceconfig.Bootstrap) string {
	n, err := spaceconfig.DataDiskBytes(boot.VM.DataDisk)
	if err != nil {
		return boot.VM.DataDisk
	}
	return spaceconfig.DiskSize(n)
}
