package openshell

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestRuntimeViewDetachedMountAcrossNamespace(t *testing.T) {
	const childMarker = "YOLOCTL_TEST_PRIVATE_MOUNT_CHILD"
	if os.Getenv(childMarker) != "1" {
		if os.Geteuid() != 0 {
			t.Skip("isolated runtime mount regression requires root CAP_SYS_ADMIN")
		}
		cmd := exec.Command(os.Args[0], "-test.run=^TestRuntimeViewDetachedMountAcrossNamespace$", "-test.v")
		cmd.Env = append(os.Environ(), childMarker+"=1")
		cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNS}
		output, err := cmd.CombinedOutput()
		if errors.Is(err, unix.EPERM) {
			t.Skip("executor cannot create the isolated mount namespace")
		}
		if err != nil {
			t.Fatalf("isolated mount regression: %v\n%s", err, output)
		}
		return
	}
	// Already isolated from the test executor before any mount. Never unlock
	// this thread or let a shared host/VM mount receive a test mutation.
	runtime.LockOSThread()
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		t.Fatal(err)
	}
	sourceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceDir, "marker"), []byte("original-source"), 0600); err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(sourceDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
	var views []*os.File
	for range 2 {
		view, err := cloneRuntimeView(source)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = view.Close() }()
		views = append(views, view)
	}
	// Match the production order: retained/cloned sources precede unshare,
	// then a new tmpfs hides the old tree before attachment.
	if err := unix.Unshare(unix.CLONE_NEWNS); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := unix.Mount("runtime-view-test", root, "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "mode=0700,size=1m"); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Unmount(root, unix.MNT_DETACH) }()
	for i, row := range []struct {
		name             string
		readOnly, noExec bool
	}{{"artifacts", true, false}, {"control", false, true}} {
		target := filepath.Join(root, row.name)
		if err := os.Mkdir(target, 0700); err != nil {
			t.Fatal(err)
		}
		if err := bindRuntimeView(source, target, row.readOnly, row.noExec); !errors.Is(err, unix.EINVAL) {
			t.Fatalf("original cross-namespace bind did not reproduce EINVAL: %v", err)
		}
		if err := attachRuntimeView(views[i], target, row.readOnly, row.noExec); err != nil {
			t.Fatal(err)
		}
		body, err := os.ReadFile(filepath.Join(target, "marker"))
		if err != nil || string(body) != "original-source" {
			t.Fatalf("detached view lost original bytes: %q %v", body, err)
		}
		var original, mounted unix.Stat_t
		if unix.Fstat(int(source.Fd()), &original) != nil || unix.Stat(target, &mounted) != nil || original.Dev != mounted.Dev || original.Ino != mounted.Ino {
			t.Fatal("detached view changed the original source inode")
		}
		err = os.WriteFile(filepath.Join(target, "write-control"), []byte("owned"), 0600)
		if (row.readOnly && !errors.Is(err, unix.EROFS)) || (!row.readOnly && err != nil) {
			t.Fatalf("wrong view write behavior: %v", err)
		}
	}
}
