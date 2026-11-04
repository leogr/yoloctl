package vmguest

import (
	"bytes"
	"crypto/sha256"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diskfs/go-diskfs/backend/file"
	"github.com/diskfs/go-diskfs/filesystem/ext4"
	"github.com/leogr/yoloctl/internal/ext4sb"
	"github.com/leogr/yoloctl/internal/vmwire"
)

// formatted is a data disk formatted like the host's formatDisk, extended
// from 1 GiB to 2 GiB as the host extends it, with one file written by
// debugfs when e2fsprogs is present.
func formatted(t *testing.T) (string, vmwire.DataDiskGrow, []byte) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "data.ext4")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(1 << 30); err != nil {
		t.Fatal(err)
	}
	if _, err := ext4.Create(file.New(f, false), 1<<30, 0, 512, &ext4.Params{
		SectorsPerBlock: 8, InodeRatio: 16384,
		Features: []ext4.FeatureOpt{ext4.WithFeatureReservedGDTBlocksForExpansion(false)},
	}); err != nil {
		t.Fatal(err)
	}
	sb, err := ext4sb.Read(f)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	var sum []byte
	if debugfs, err := exec.LookPath("/usr/sbin/debugfs"); err == nil {
		blob := filepath.Join(t.TempDir(), "blob")
		data := bytes.Repeat([]byte("declaration\n"), 50000)
		if err := os.WriteFile(blob, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command(debugfs, "-w", "-R", "write "+blob+" kept", path).CombinedOutput(); err != nil {
			t.Fatalf("debugfs: %v %s", err, out)
		}
		s := sha256.Sum256(data)
		sum = s[:]
	}
	if err := os.Truncate(path, 2<<30); err != nil {
		t.Fatal(err)
	}
	return path, vmwire.DataDiskGrow{UUID: sb.UUID, From: 1 << 30, To: 2 << 30}, sum
}

// With e2fsprogs, the offline grow keeps the UUID and the data and leaves a
// clean filesystem of the new size; a second boot finds it grown and only
// checks it again.
func TestGrowDataDiskWithE2fsprogs(t *testing.T) {
	for _, tool := range []string{"/usr/sbin/e2fsck", "/usr/sbin/resize2fs", "/usr/sbin/debugfs"} {
		if _, err := os.Stat(tool); err != nil {
			t.Skip("e2fsprogs not installed")
		}
	}
	path, g, sum := formatted(t)
	if err := growDataDisk(path, g, runGrowTool); err != nil {
		t.Fatal(err)
	}
	sb, size, err := deviceState(path)
	if err != nil || sb.UUID != g.UUID || sb.Size != g.To || size != g.To {
		t.Fatalf("after: %+v %d %v", sb, size, err)
	}
	out := filepath.Join(t.TempDir(), "out")
	if o, err := exec.Command("/usr/sbin/debugfs", "-R", "dump kept "+out, path).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, o)
	}
	data, err := os.ReadFile(out)
	if s := sha256.Sum256(data); err != nil || !bytes.Equal(s[:], sum) {
		t.Fatalf("the file changed: %v", err)
	}
	var tools []string
	if err := growDataDisk(path, g, func(name string, args ...string) (int, error) {
		tools = append(tools, filepath.Base(name)+" "+strings.Join(args[:len(args)-1], " "))
		return runGrowTool(name, args...)
	}); err != nil || strings.Join(tools, ",") != "e2fsck -f -p,e2fsck -f -n" {
		t.Fatalf("an already grown disk is only checked: %v %v", tools, err)
	}
}

// Every unexpected disk or tool outcome refuses, and the resize never runs
// before a clean repair check.
func TestGrowDataDiskRefusals(t *testing.T) {
	path, g, _ := formatted(t)
	ok := func(string, ...string) (int, error) { return 0, nil }
	for _, tc := range []struct {
		name string
		grow vmwire.DataDiskGrow
		run  growTool
		want string
	}{
		{"another filesystem", vmwire.DataDiskGrow{UUID: "00000000-0000-0000-0000-000000000000", From: g.From, To: g.To}, ok, "is not the space's data disk"},
		{"disk not extended", vmwire.DataDiskGrow{UUID: g.UUID, From: g.From, To: 3 << 30}, ok, "disk is 2147483648 bytes, expected 3221225472"},
		{"filesystem of another size", vmwire.DataDiskGrow{UUID: g.UUID, From: 1<<30 + 1<<20, To: g.To}, ok, "filesystem is 1073741824 bytes"},
		{"repair needs a person", g, func(name string, _ ...string) (int, error) {
			if strings.HasSuffix(name, "resize2fs") {
				t.Fatal("resized after a failed check")
			}
			return 4, nil
		}, "e2fsck -f -p exited 4"},
		{"resize fails", g, func(name string, _ ...string) (int, error) {
			if strings.HasSuffix(name, "resize2fs") {
				return 1, nil
			}
			return 0, nil
		}, "resize2fs exited 1"},
		{"after-check not clean", g, func(name string, args ...string) (int, error) {
			if len(args) > 1 && args[1] == "-n" {
				return 4, nil
			}
			return 0, nil
		}, "e2fsck -f -n exited 4"},
		{"size unchanged after", g, ok, "after the grow the filesystem is"},
	} {
		if err := growDataDisk(path, tc.grow, tc.run); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
}
