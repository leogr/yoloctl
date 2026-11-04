package lima

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/layout"
)

func TestAccountDiskYAMLPreservesOtherDisksAndComments(t *testing.T) {
	id := "0123456789abcdef0123456789abcdef"
	path := filepath.Join(t.TempDir(), "lima.yaml")
	raw := "# VM settings\ncpus: 4 # retain\nadditionalDisks:\n  - name: custom # unrelated\n    format: false\n    fsType: xfs\n  - name: " + layout.AccountDiskName(id) + " # identity\n    format: true # only first boot\n    fsType: ext4\n    fsArgs: [\"-m\", \"0\"]\nmounts: []\n"
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	if err := setInstanceAccountDisk(path, id, false); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"# VM settings", "cpus: 4 # retain", "name: custom # unrelated", "fsType: xfs", "# identity", "format: false # only first boot", "fsArgs:", "mounts: []"} {
		if !bytes.Contains(first, []byte(text)) {
			t.Fatalf("lost %q: %s", text, first)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := setInstanceAccountDisk(path, id, false); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(info, after) || !bytes.Equal(first, second) {
		t.Fatal("unchanged YAML rewritten")
	}
}

func TestAccountDiskBlankRefusesDataSymlinksAndAttachment(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	id := "0123456789abcdef0123456789abcdef"
	dir, err := layout.AccountDiskDir(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "limactl")
	script := "#!/bin/sh\nprintf '%s\\n' '{\"name\":\"" + layout.AccountDiskName(id) + "\",\"format\":\"raw\",\"instance\":\"'\"${TEST_DISK_OWNER:-}\"'\"}'\n"
	if err := os.WriteFile(binary, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	c := New()
	c.binary = binary
	path := filepath.Join(dir, "datadisk")
	if err := os.WriteFile(path, make([]byte, 2*1024*1024), 0600); err != nil {
		t.Fatal(err)
	}
	if blank, err := c.AccountDiskBlank(t.Context(), id); err != nil || !blank {
		t.Fatalf("blank: %v %v", blank, err)
	}
	// Even data far from the usual filesystem signatures forbids formatting.
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte{1}, 1500000); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if blank, err := c.AccountDiskBlank(t.Context(), id); err != nil || blank {
		t.Fatalf("data: %v %v", blank, err)
	}
	t.Setenv("TEST_DISK_OWNER", "yolo-work")
	if blank, err := c.AccountDiskBlank(t.Context(), id); err == nil || blank {
		t.Fatalf("attached: %v %v", blank, err)
	}
	t.Setenv("TEST_DISK_OWNER", "")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, make([]byte, 4096), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if blank, err := c.AccountDiskBlank(t.Context(), id); err == nil || blank {
		t.Fatalf("symlink: %v %v", blank, err)
	}
}

func TestBrokenVMShutdownChecksProcessesBeforeAndAfterStop(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir, err := InstanceDir("broken")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	pid := filepath.Join(dir, "ha.pid")
	if err := os.WriteFile(pid, []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := VMProcessIDs("broken")
	if err != nil {
		t.Fatal(err)
	}
	if err := RequireVMProcessesStopped("broken", "qemu", before); err == nil {
		t.Fatal("accepted live PID")
	}
	if err := os.Remove(pid); err != nil {
		t.Fatal(err)
	}
	if err := RequireVMProcessesStopped("broken", "qemu", before); err == nil {
		t.Fatal("forgot live PID after file removal")
	}
	if err := RequireVMProcessesStopped("broken", "qemu", nil); err != nil {
		t.Fatal(err)
	}
	if err := RequireVMProcessesStopped("broken", "unknown", nil); err == nil {
		t.Fatal("accepted unknown backend")
	}
	if err := os.WriteFile(pid, []byte("not-a-pid"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := VMProcessIDs("broken"); err == nil || !strings.Contains(err.Error(), "invalid VM PID") {
		t.Fatal(err)
	}
}
