//go:build linux && amd64

package firecracker

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/leogr/yoloctl/internal/vmapi"
)

func resourceFixture(t *testing.T, name string) (string, Config) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir, e := Driver{}.InstanceDir(name)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.MkdirAll(dir, 0700); e != nil {
		t.Fatal(e)
	}
	c := Config{Schema: 1, Name: name, UID: 1000, GID: 1000, Executable: "/opt/yoloctl",
		Spec:       vmapi.Spec{DiskID: "disk-id", CPUs: 4, Memory: "4GiB", Disk: "30GiB", Image: "/images/one", Mounts: []vmapi.Mount{{Location: "/work", Writable: true}}},
		ImageFiles: map[string]artifactStamp{"kernel": {Inode: 7}}}
	if e = WriteConfig(dir, c); e != nil {
		t.Fatal(e)
	}
	return dir, c
}

func inode(t *testing.T, path string) uint64 {
	t.Helper()
	st, e := os.Stat(path)
	if e != nil {
		t.Fatal(e)
	}
	return st.Sys().(*syscall.Stat_t).Ino
}

func requireConfig(t *testing.T, dir string, want Config) {
	t.Helper()
	got, e := ReadConfig(dir)
	if e != nil {
		t.Fatal(e)
	}
	if got.Name != want.Name || got.UID != want.UID || got.GID != want.GID || got.Executable != want.Executable ||
		got.Spec.DiskID != want.Spec.DiskID || got.Spec.Disk != want.Spec.Disk || got.Spec.Image != want.Spec.Image || got.Spec.Format != want.Spec.Format ||
		len(got.Spec.Mounts) != 1 || got.Spec.Mounts[0] != want.Spec.Mounts[0] || len(got.ImageFiles) != 1 || got.ImageFiles["kernel"] != want.ImageFiles["kernel"] ||
		got.Spec.CPUs != want.Spec.CPUs || got.Spec.Memory != want.Spec.Memory {
		t.Fatalf("configuration changed beyond resources:\n got %+v\nwant %+v", got, want)
	}
}

func TestSetResourcesRewritesOnlyStoppedBootResources(t *testing.T) {
	dir, c := resourceFixture(t, "dev")
	d := Driver{}
	if e := d.SetResources(t.Context(), "dev", vmapi.Resources{CPUs: 8, Memory: "12GiB"}); e != nil {
		t.Fatal(e)
	}
	c.Spec.CPUs, c.Spec.Memory = 8, "12GiB"
	requireConfig(t, dir, c)
	path := filepath.Join(dir, "vm.json")
	before := inode(t, path)
	if e := d.SetResources(t.Context(), "dev", vmapi.Resources{CPUs: 8, Memory: "12GiB"}); e != nil {
		t.Fatal(e)
	}
	if inode(t, path) != before {
		t.Fatal("equal resources rewrote the configuration")
	}
	for _, r := range []vmapi.Resources{{CPUs: 0, Memory: "4GiB"}, {CPUs: 33, Memory: "4GiB"}, {CPUs: 2, Memory: "64MiB"}, {CPUs: 2, Memory: "4097KiB"}, {CPUs: 2, Memory: "4G"}, {CPUs: 2, Memory: ""}} {
		if e := d.SetResources(t.Context(), "dev", r); e == nil || errors.Is(e, vmapi.ErrNotStopped) {
			t.Fatalf("%+v: %v", r, e)
		}
	}
	requireConfig(t, dir, c)
}

func TestSetResourcesRefusesLiveUncertainAndLockedVMs(t *testing.T) {
	dir, c := resourceFixture(t, "dev")
	d := Driver{}
	change := vmapi.Resources{CPUs: 2, Memory: "2GiB"}
	me, e := identity(os.Getpid())
	if e != nil {
		t.Fatal(e)
	}
	// A live helper also covers a VM paused for host storage: its helper stays.
	if e = writeState(dir, runtimeState{Helper: me, Dir: "/tmp/yfc-fixture"}); e != nil {
		t.Fatal(e)
	}
	if e = d.SetResources(t.Context(), "dev", change); !errors.Is(e, vmapi.ErrNotStopped) {
		t.Fatalf("live VM: %v", e)
	}
	dead := me
	dead.Start += "0"
	if e = writeState(dir, runtimeState{Helper: dead, Dir: "/tmp/yfc-fixture", LaunchPending: true}); e != nil {
		t.Fatal(e)
	}
	if e = d.SetResources(t.Context(), "dev", change); !errors.Is(e, vmapi.ErrNotStopped) {
		t.Fatalf("uncertain launch: %v", e)
	}
	if e = os.Remove(filepath.Join(dir, "runtime.json")); e != nil && !errors.Is(e, os.ErrNotExist) {
		t.Fatal(e)
	}
	if _, e = readState(dir); !errors.Is(e, os.ErrNotExist) {
		t.Fatalf("fixture state remains: %v", e)
	}
	lock, e := lockFile(filepath.Join(dir, "lock"))
	if e != nil {
		t.Fatal(e)
	}
	if e = d.SetResources(t.Context(), "dev", change); !errors.Is(e, vmapi.ErrNotStopped) {
		t.Fatalf("starting VM holding its lock: %v", e)
	}
	_ = lock.Close()
	requireConfig(t, dir, c)
	if e = d.SetResources(t.Context(), "dev", change); e != nil {
		t.Fatal(e)
	}
}

func TestSetResourcesRequiresTheSpacesOwnInstance(t *testing.T) {
	dir, c := resourceFixture(t, "dev")
	d := Driver{}
	missing, e := d.InstanceDir("absent")
	if e != nil {
		t.Fatal(e)
	}
	if e = d.SetResources(t.Context(), "absent", vmapi.Resources{CPUs: 2, Memory: "2GiB"}); e == nil || errors.Is(e, vmapi.ErrNotStopped) {
		t.Fatalf("missing instance: %v", e)
	}
	if _, e = os.Lstat(missing); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("refused change created instance state")
	}
	other := c
	other.Name = "other"
	if e = WriteConfig(dir, other); e != nil {
		t.Fatal(e)
	}
	if e = d.SetResources(t.Context(), "dev", vmapi.Resources{CPUs: 2, Memory: "2GiB"}); e == nil {
		t.Fatal("rewrote another space's configuration")
	}
	requireConfig(t, dir, other)
}
