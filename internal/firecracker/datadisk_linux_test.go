//go:build linux && amd64

package firecracker

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/leogr/yoloctl/internal/ext4sb"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/internal/vmapi"
	"github.com/leogr/yoloctl/internal/vmwire"
)

const diskID = "11111111111111111111111111111111"

// dataDiskFixture is a stopped instance whose 1 GiB data disk is formatted
// like a real one.
func dataDiskFixture(t *testing.T) (Driver, string, ext4sb.Superblock) {
	t.Helper()
	dir, c := resourceFixture(t, "dev")
	c.Spec.DiskID = diskID
	if err := WriteConfig(dir, c); err != nil {
		t.Fatal(err)
	}
	d := Driver{}
	if err := d.CreateAccountDisk(t.Context(), diskID, 1<<30); err != nil {
		t.Fatal(err)
	}
	data, err := d.AccountDiskDir(diskID)
	if err != nil {
		t.Fatal(err)
	}
	if err := formatDisk(t.Context(), filepath.Join(data, dataDiskFile)); err != nil {
		t.Fatal(err)
	}
	sb, size, err := d.DataDiskState("dev", diskID)
	if err != nil || size != 1<<30 || sb.Size != 1<<30 {
		t.Fatalf("%+v %d %v", sb, size, err)
	}
	_ = dir
	return d, data, sb
}

// The host extends a stopped VM's data disk file and nothing else; an
// already extended file is left alone, and an unexpected size, a held lock
// or a host without reflink (tmpfs) for a requested copy refuses unchanged.
func TestExtendDataDiskOnlyGrowsAStoppedDisk(t *testing.T) {
	d, data, sb := dataDiskFixture(t)
	path := filepath.Join(data, dataDiskFile)
	if err := d.ExtendDataDisk("dev", diskID, 2<<30, 3<<30, false); err == nil {
		t.Fatal("extended from a size the file does not have")
	}
	idir, _ := d.InstanceDir("dev")
	lock, err := lockFile(filepath.Join(idir, "lock"))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.ExtendDataDisk("dev", diskID, 1<<30, 2<<30, false); !errors.Is(err, vmapi.ErrNotStopped) {
		t.Fatalf("a starting VM's disk was extended: %v", err)
	}
	if _, _, err := d.DataDiskState("dev", diskID); !errors.Is(err, vmapi.ErrNotStopped) {
		t.Fatalf("a starting VM's disk was read: %v", err)
	}
	_ = lock.Close()
	err = d.ExtendDataDisk("dev", diskID, 1<<30, 2<<30, true)
	switch {
	case errors.Is(err, vmapi.ErrNoReflink):
		if _, e := os.Stat(filepath.Join(data, dataDiskBackup)); !errors.Is(e, os.ErrNotExist) {
			t.Fatalf("a failed copy left a file: %v", e)
		}
		if st, _ := os.Stat(path); st.Size() != 1<<30 {
			t.Fatal("extended although the requested copy failed")
		}
		err = d.ExtendDataDisk("dev", diskID, 1<<30, 2<<30, false)
	case err == nil:
		if st, e := os.Stat(filepath.Join(data, dataDiskBackup)); e != nil || st.Size() != 1<<30 {
			t.Fatalf("reflink copy: %v", e)
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	after, size, err := d.DataDiskState("dev", diskID)
	if err != nil || size != 2<<30 || after != sb {
		t.Fatalf("the file grows, the filesystem waits for init: %+v %d %v", after, size, err)
	}
	if err := d.ExtendDataDisk("dev", diskID, 1<<30, 2<<30, false); err != nil {
		t.Fatalf("an extended file is left alone: %v", err)
	}
	if err := d.RemoveDataDiskBackup(diskID); err != nil {
		t.Fatal(err)
	}
}

// The boot record carries a grow only in its extended phase, for the disk
// the instance attaches; the image says whether its init grows at all; the
// console line is found in the log.
func TestDataDiskGrowReachesInitOnlyWhenExtended(t *testing.T) {
	d, _, sb := dataDiskFixture(t)
	c, err := ReadConfig(func() string { dir, _ := d.InstanceDir("dev"); return dir }())
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := spaceconfig.New("dev", "/projects", "personal")
	if err != nil {
		t.Fatal(err)
	}
	cfg.AccountDiskID, cfg.AccountDiskUUID = diskID, sb.UUID
	boot, _, err := spaceconfig.Split(cfg, 1000, 1000, "generation")
	if err != nil {
		t.Fatal(err)
	}
	boot.VM.Backend, boot.VM.Image, boot.VM.DataDisk = "firecracker", "/images/one", "5GiB"
	boot.DataDiskGrow = &spaceconfig.DataDiskGrow{From: 4 << 30, To: 5 << 30, Phase: spaceconfig.GrowRequested}
	if err := boot.Save(); err != nil {
		t.Fatal(err)
	}
	if g, err := pendingDataDiskGrow(c); err != nil || g != nil {
		t.Fatalf("a requested grow reached init: %+v %v", g, err)
	}
	boot.DataDiskGrow.Phase = spaceconfig.GrowExtended
	if err := boot.Save(); err != nil {
		t.Fatal(err)
	}
	g, err := pendingDataDiskGrow(c)
	if err != nil || g == nil || *g != (vmwire.DataDiskGrow{UUID: sb.UUID, From: 4 << 30, To: 5 << 30}) {
		t.Fatalf("%+v %v", g, err)
	}
	c.Spec.DiskID = "other"
	if _, err := pendingDataDiskGrow(c); err == nil {
		t.Fatal("a grow for another disk reached init")
	}
	if ok, err := d.DataDiskGrowSupported("dev", "/images/one"); err != nil || ok {
		t.Fatalf("an instance pinned to an older image: %v %v", ok, err)
	}
	idir, _ := d.InstanceDir("dev")
	c.Spec.DiskID = diskID
	c.Image.DataDiskGrow = DataDiskGrowVersion
	if err := WriteConfig(idir, c); err != nil {
		t.Fatal(err)
	}
	if ok, err := d.DataDiskGrowSupported("dev", "/images/one"); err != nil || !ok {
		t.Fatalf("a grow-capable image: %v %v", ok, err)
	}
	line := g.GrownLine()
	if ok, _ := d.DataDiskGrowLogged("dev", line); ok {
		t.Fatal("no log yet")
	}
	if err := os.WriteFile(filepath.Join(idir, "vm.log"), []byte("boot\n"+line+"\nready\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if ok, err := d.DataDiskGrowLogged("dev", line); err != nil || !ok {
		t.Fatalf("logged: %v %v", ok, err)
	}
}
