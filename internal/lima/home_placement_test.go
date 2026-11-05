package lima

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/spaceconfig"
)

func placementBootstrap(t *testing.T, name, home, id string) *spaceconfig.Bootstrap {
	t.Helper()
	b := &spaceconfig.Bootstrap{SchemaVersion: 3, Name: name, DiskID: strings.Repeat(id, 32), DiskUUID: "unchanged-uuid", UID: 1000, GID: 20, Generation: "generation", VM: spaceconfig.VMSettings{Backend: "lima", LimaHome: home, CPUs: 2, Memory: "4GiB", Disk: "30GiB"}}
	if err := b.Save(); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestLimaCommandsAndDisksUseEachBootstrapHome(t *testing.T) {
	base := socketTestRoot(t)
	t.Setenv("YOLOCTL_HOME", base)
	legacy, _ := layout.LimaHome()
	next := filepath.Join(base, "new")
	old := placementBootstrap(t, strings.Repeat("a", 32), "", "1")
	fresh := placementBootstrap(t, "sixteenchar-name", next, "2")
	calls := filepath.Join(base, "calls")
	t.Setenv("PLACEMENT_CALLS", calls)
	binary := filepath.Join(base, "limactl")
	script := `#!/bin/sh
printf '%s\t%s\n' "$LIMA_HOME" "$*" >> "$PLACEMENT_CALLS"
if [ "$1" = list ]; then
 for d in "$LIMA_HOME"/yolo-*; do
  [ -d "$d" ] || continue
  printf '{"name":"%s","status":"Stopped","vmType":"qemu"}\n' "${d##*/}"
 done
fi
`
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	c := &Client{binary: binary, stdout: io.Discard}
	t.Setenv("PATH", base+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("LIMA_HOME", "/must-not-be-used")
	for i, b := range []*spaceconfig.Bootstrap{old, fresh} {
		home := []string{legacy, next}[i]
		dir, err := InstanceDir(b.Name)
		if err != nil || dir != filepath.Join(home, InstanceName(b.Name)) {
			t.Fatalf("instance %s %v", dir, err)
		}
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		diskDir, err := AccountDiskDir(b.DiskID)
		if err != nil || diskDir != filepath.Join(home, "_disks", layout.AccountDiskName(b.DiskID)) {
			t.Fatalf("disk %s %v", diskDir, err)
		}
		for _, err := range []error{c.Create(t.Context(), b.Name, nil), c.Start(t.Context(), b.Name), c.Stop(t.Context(), b.Name), c.CopyTo(t.Context(), b.Name, "local", "/remote"), c.CreateAccountDisk(t.Context(), b.DiskID, 1<<30), c.Delete(t.Context(), b.Name, true)} {
			if err != nil {
				t.Fatal(err)
			}
		}
		if _, err := c.AccountDisk(t.Context(), b.DiskID); err != nil {
			t.Fatal(err)
		}
		if err := c.DeleteAccountDisk(t.Context(), b.DiskID); err != nil {
			t.Fatal(err)
		}
		if err := ShellCommand(t.Context(), b.Name, "true").Run(); err != nil {
			t.Fatal(err)
		}
		if err := ShellCommandWithWorkdir(t.Context(), b.Name, "/work", "true").Run(); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := c.List(t.Context())
	if err != nil || len(rows) != 2 {
		t.Fatalf("mixed list: %v %v", rows, err)
	}
	raw, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 22 {
		t.Fatalf("unexpected calls: %s", raw)
	}
	for i, line := range lines[:20] {
		home := legacy
		if i >= 10 {
			home = next
		}
		if !strings.HasPrefix(line, home+"\t") {
			t.Fatalf("cross-home command %q", line)
		}
	}
	if os.Getenv("LIMA_HOME") != "/must-not-be-used" {
		t.Fatal("changed process environment")
	}
	// Duplicate physical instances must not silently select one or be cleaned up.
	if err := os.Mkdir(filepath.Join(next, InstanceName(old.Name)), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := c.List(t.Context()); err == nil {
		t.Fatal("accepted conflicting homes")
	}
}

func TestPreflightUsesRecordedHomeAndKeepsLegacyReads(t *testing.T) {
	base := socketTestRoot(t)
	t.Setenv("YOLOCTL_HOME", base)
	long := filepath.Join(base, strings.Repeat("x", 100))
	b := placementBootstrap(t, "target", long, "3")
	if err := ValidateInstancePaths(b.Name); err == nil || !strings.Contains(err.Error(), long) || !strings.Contains(err.Error(), "one-character name") {
		t.Fatalf("recorded preflight: %v", err)
	}
	if _, err := os.Stat(long); !os.IsNotExist(err) {
		t.Fatalf("preflight created home: %v", err)
	}
	if _, err := InstanceDir(b.Name); err != nil {
		t.Fatal("reads use boot preflight", err)
	}
	b.VM.LimaHome = ""
	if err := b.Save(); err != nil {
		t.Fatal(err)
	}
	if err := ValidateInstancePaths(b.Name); err != nil {
		t.Fatal("legacy fallback did not fit", err)
	}
}

func TestLimaOrphanOutsideLegacyHomeRequiresBootstrapRecovery(t *testing.T) {
	base := socketTestRoot(t)
	t.Setenv("YOLOCTL_HOME", base)
	next := filepath.Join(base, "recorded")
	placementBootstrap(t, "owner", next, "4")
	if err := os.MkdirAll(filepath.Join(next, "yolo-orphan"), 0700); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(base, "limactl")
	if err := os.WriteFile(script, []byte(`#!/bin/sh
[ "$LIMA_HOME" = "$ORPHAN_HOME" ] || exit 0
printf '%s\n' '{"name":"yolo-orphan","status":"Stopped"}'
`), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ORPHAN_HOME", next)
	c := &Client{binary: script, stdout: io.Discard}
	if _, err := c.List(t.Context()); err == nil || !strings.Contains(err.Error(), "restore its bootstrap") {
		t.Fatalf("unsafe orphan routing: %v", err)
	}
}
