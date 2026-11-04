package openshell

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestRuntimeBoundaryCAStorageUsesWorkloadOwnershipAndPublicMaterial(t *testing.T) {
	root, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	source, err := createBoundaryCAStorage(root, os.Geteuid(), os.Getegid())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
	var st unix.Stat_t
	if err := unix.Fstat(int(source.Fd()), &st); err != nil {
		t.Fatal(err)
	}
	if st.Mode&0o7777 != 0o755 || st.Uid != uint32(os.Geteuid()) || st.Gid != uint32(os.Getegid()) {
		t.Fatalf("CA source ownership: %+v", st)
	}
	// Match upstream's fixed material child and temporary-file rename flow.
	dir := filepath.Join(root.Name(), "boundary-ca", "material")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ca.crt", "ca-bundle.crt"} {
		if err := os.WriteFile(filepath.Join(dir, name+".tmp"), []byte("public certificate"), 0444); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(filepath.Join(dir, name+".tmp"), filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := createBoundaryCAStorage(root, os.Geteuid(), os.Getegid()); !errors.Is(err, unix.EEXIST) {
		t.Fatalf("existing CA source adopted: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "ca.crt")); err != nil || string(b) != "public certificate" {
		t.Fatal("original material changed", err)
	}
}

func TestRuntimeBoundaryCAMountpointKeepsParentPermissions(t *testing.T) {
	run, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = run.Close() }()
	before, err := run.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureBoundaryCAMountpoint(run, uint32(os.Geteuid()), uint32(os.Getegid())); err != nil {
		t.Fatal(err)
	}
	after, err := run.Stat()
	if err != nil || before.Mode() != after.Mode() {
		t.Fatal("broadened run parent", err)
	}
	entries, err := os.ReadDir(run.Name())
	if err != nil || len(entries) != 1 || entries[0].Name() != "openshell-supervisor-ca" {
		t.Fatal("unexpected run leaf", err)
	}
}

func TestRuntimeBoundaryCAMountpointRejectsSymlinkAndForeignWriter(t *testing.T) {
	for _, kind := range []string{"symlink", "writable", "file"} {
		t.Run(kind, func(t *testing.T) {
			run, err := os.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = run.Close() }()
			leaf := filepath.Join(run.Name(), "openshell-supervisor-ca")
			switch kind {
			case "symlink":
				err = os.Symlink(t.TempDir(), leaf)
			case "file":
				err = os.WriteFile(leaf, []byte("retained"), 0600)
			case "writable":
				err = os.Mkdir(leaf, 0700)
				if err == nil {
					err = os.Chmod(leaf, 0777)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := ensureBoundaryCAMountpoint(run, uint32(os.Geteuid()), uint32(os.Getegid())); err == nil {
				t.Fatal("untrusted mountpoint accepted")
			}
		})
	}
}
