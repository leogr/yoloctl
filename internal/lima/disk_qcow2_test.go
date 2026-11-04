package lima

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/layout"
)

const testAccountDiskID = "0123456789abcdef0123456789abcdef"

func qcow2Fixture(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/blank.qcow2.gz")
	if err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = zr.Close() }()
	data, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func qcow2TestDisk(t *testing.T) (*Client, string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir, err := layout.AccountDiskDir(testAccountDiskID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "limactl")
	// Deliberately model Lima reporting qcow2 despite disk create --format raw.
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' '{\"name\":\"%s\",\"format\":\"qcow2\",\"instance\":\"\"}'\n", layout.AccountDiskName(testAccountDiskID))
	if err := os.WriteFile(binary, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	c := New()
	c.binary = binary
	return c, filepath.Join(dir, "datadisk")
}

func TestAccountDiskBlankQCOW2(t *testing.T) {
	const cluster = 65536
	for _, tc := range []struct {
		name    string
		edit    func([]byte) []byte
		blank   bool
		errText string
	}{
		{name: "blank metadata", blank: true},
		{name: "version 2", blank: true, edit: func(b []byte) []byte { binary.BigEndian.PutUint32(b[4:], 2); return b }},
		{name: "nonzero guest sector", edit: func(b []byte) []byte {
			b = append(b, make([]byte, 2*cluster)...)
			binary.BigEndian.PutUint64(b[3*cluster:], 1<<63|4*cluster)
			// Data beyond the usual filesystem signatures must also prevent formatting.
			binary.BigEndian.PutUint64(b[4*cluster+23*8:], 1<<63|5*cluster)
			b[5*cluster+17] = 0x7f
			return b
		}},
		{name: "backing file", edit: func(b []byte) []byte {
			binary.BigEndian.PutUint64(b[8:], 256)
			binary.BigEndian.PutUint32(b[16:], uint32(len("/dev/zero")))
			copy(b[256:], "/dev/zero")
			return b
		}, errText: "backing files"},
		{name: "snapshot", edit: func(b []byte) []byte { binary.BigEndian.PutUint32(b[60:], 1); return b }, errText: "snapshots"},
		{name: "encryption", edit: func(b []byte) []byte { binary.BigEndian.PutUint32(b[32:], 1); return b }, errText: "encryption"},
		{name: "dirty", edit: func(b []byte) []byte { binary.BigEndian.PutUint64(b[72:], 1); return b }, errText: "dirty"},
		{name: "corrupt", edit: func(b []byte) []byte { binary.BigEndian.PutUint64(b[72:], 2); return b }, errText: "metadata"},
		{name: "external data file", edit: func(b []byte) []byte { binary.BigEndian.PutUint64(b[72:], 4); return b }, errText: "metadata"},
		{name: "unknown version", edit: func(b []byte) []byte { binary.BigEndian.PutUint32(b[4:], 4); return b }, errText: "unsupported"},
		{name: "truncated L1 table", edit: func(b []byte) []byte { return b[:3*cluster] }, errText: "truncated"},
		{name: "unreadable guest cluster", edit: func(b []byte) []byte {
			binary.BigEndian.PutUint64(b[3*cluster:], 1<<63|6*cluster)
			return b
		}, errText: "declared size"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, path := qcow2TestDisk(t)
			data := qcow2Fixture(t)
			if tc.edit != nil {
				data = tc.edit(data)
			}
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			blank, err := c.AccountDiskBlank(t.Context(), testAccountDiskID)
			if blank != tc.blank {
				t.Fatalf("blank=%v, want %v (err=%v)", blank, tc.blank, err)
			}
			if tc.errText == "" && err != nil {
				t.Fatal(err)
			}
			if tc.errText != "" && (err == nil || !strings.Contains(err.Error(), tc.errText)) {
				t.Fatalf("error=%v, want %s", err, tc.errText)
			}
			after, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if !bytes.Equal(data, after) {
				t.Fatal("read-only blank check changed image")
			}
		})
	}
}

// Hosts with QEMU additionally verify the synthetic fixture and exercise actual
// qemu-img output plus qemu-io writes, independently of our boundary fake.
func TestAccountDiskBlankWithQEMUTools(t *testing.T) {
	for _, tool := range []string{"qemu-img", "qemu-io"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s required for QEMU image integration", tool)
		}
	}
	c, path := qcow2TestDisk(t)
	run := func(tool string, args ...string) {
		t.Helper()
		if out, err := exec.CommandContext(t.Context(), tool, args...).CombinedOutput(); err != nil {
			t.Fatalf("%s %v: %v: %s", tool, args, err, out)
		}
	}
	if err := os.WriteFile(path, qcow2Fixture(t), 0600); err != nil {
		t.Fatal(err)
	}
	run("qemu-img", "check", "-f", "qcow2", path)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	run("qemu-img", "create", "-f", "qcow2", path, "2M")
	if blank, err := c.AccountDiskBlank(t.Context(), testAccountDiskID); err != nil || !blank {
		t.Fatalf("fresh QEMU image: blank=%v err=%v", blank, err)
	}
	run("qemu-io", "-f", "qcow2", "-c", "write -P 127 1500000 512", path)
	if blank, err := c.AccountDiskBlank(t.Context(), testAccountDiskID); err != nil || blank {
		t.Fatalf("written QEMU image: blank=%v err=%v", blank, err)
	}
}
