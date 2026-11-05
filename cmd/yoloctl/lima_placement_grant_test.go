package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	vmhost "github.com/leogr/yoloctl/internal/vm"
)

// Models an existing macOS space sharing ~/.config before new Lima storage
// is published there. Explicit home fixtures keep this test host-independent.
func TestLimaPlacementRejectsExistingGrantBeforeDiskAllocation(t *testing.T) {
	stubOperatorClient(t, nil)
	root := t.TempDir()
	t.Setenv("YOLOCTL_HOME", filepath.Join(root, "legacy-config"))
	shared := filepath.Join(root, "dot-config")
	project := filepath.Join(root, "project")
	for _, p := range []string{shared, project} {
		if err := os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	old := imageUpdateFixture(t)
	old.Name = "old"
	old.VM.Backend, old.VM.Image, old.VM.LimaHome = "lima", "", ""
	old.Mounts = []spaceconfig.Mount{{Source: shared}}
	if err := old.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := controllerMounts(old); err != nil {
		t.Fatalf("old grant should be admissible before placement: %v", err)
	}
	fresh := *old
	fresh.Name = "new"
	fresh.DiskID = strings.Repeat("a", 32)
	fresh.VM.LimaHome = filepath.Join(shared, "yoloctl", "lima")
	fresh.Mounts = []spaceconfig.Mount{{Source: project}}
	fresh.Initializing, fresh.DiskUUID = true, ""
	if err := rejectSpaceHostPath(project, nil); err != nil {
		t.Fatal(err)
	}
	if err := fresh.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := controllerMounts(&fresh); err != nil {
		t.Fatal(err)
	}
	calls := filepath.Join(root, "disk-calls")
	t.Setenv("REVIEW_DISK_CALLS", calls)
	fake := filepath.Join(root, "limactl")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nprintf '%s %s\\n' \"$LIMA_HOME\" \"$*\" > \"$REVIEW_DISK_CALLS\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	err := vmhost.New().CreateAccountDisk(t.Context(), fresh.DiskID)
	raw, readErr := os.ReadFile(calls)
	requirePlacementConflict(t, err, old.Name, shared)
	if !errors.Is(readErr, os.ErrNotExist) {
		t.Fatalf("allocation reached limactl: call=%s read=%v", raw, readErr)
	}
	if _, err := os.Stat(fresh.VM.LimaHome); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("created exposed storage: %v", err)
	}
}

func requirePlacementConflict(t *testing.T, err error, space, grant string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected placement refusal")
	}
	for _, want := range []string{"Lima storage", "overlaps host grant", space, grant} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal missing %q: %v", want, err)
		}
	}
}

func placementCalls(t *testing.T, root string) string {
	t.Helper()
	calls := filepath.Join(root, "calls")
	t.Setenv("PLACEMENT_CALLS", calls)
	script := filepath.Join(root, "limactl")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s %s\\n' \"$LIMA_HOME\" \"$*\" >> \"$PLACEMENT_CALLS\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	return calls
}

func requireNoPlacementCalls(t *testing.T, calls string) {
	t.Helper()
	if raw, err := os.ReadFile(calls); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("placement reached a VM command: %q %v", raw, err)
	}
}

func TestLimaPlacementRefusalPrecedesCreateAndImportPublication(t *testing.T) {
	for _, action := range []string{"create", "import"} {
		t.Run(action, func(t *testing.T) {
			stubOperatorClient(t, nil)
			shortYoloctlHome(t)
			root, err := layout.ConfigRoot()
			if err != nil {
				t.Fatal(err)
			}
			home, err := layout.NewLimaHome()
			if err != nil {
				t.Fatal(err)
			}
			// Synthetic saved grants model the pre-existing macOS grant.
			// Admission uses the actual destination selector on every host.
			grant := filepath.Dir(home)
			old := imageUpdateFixture(t)
			old.Name = "old"
			old.VM.Backend, old.VM.Image, old.VM.LimaHome = "lima", "", ""
			old.Mounts = []spaceconfig.Mount{{Source: grant, Writable: true}}
			if err := old.Save(); err != nil {
				t.Fatal(err)
			}
			oldPath, _ := layout.SpaceConfigPath(old.Name)
			before, err := os.ReadFile(oldPath)
			if err != nil {
				t.Fatal(err)
			}
			calls := placementCalls(t, root)
			project := filepath.Join(t.TempDir(), "not-created")
			args := []string{"space", action, "new", "--backend", "lima"}
			if action == "create" {
				args = append(args, "--account", "personal", "--workdir", project)
			} else {
				args = append(args, "/missing-archive.tar.gz")
			}
			requirePlacementConflict(t, resourceCommand(t, args...), old.Name, grant)
			requireNoPlacementCalls(t, calls)
			dest, _ := layout.SpaceBundleDir("new")
			if _, err := os.Stat(dest); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("published destination: %v", err)
			}
			if _, err := os.Stat(home); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("created Lima home: %v", err)
			}
			if _, err := os.Stat(project); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("created project: %v", err)
			}
			after, err := os.ReadFile(oldPath)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("changed existing grant: %v", err)
			}
		})
	}
}

func TestLimaPlacementRefusalCoversPendingAllocationRecovery(t *testing.T) {
	for _, entry := range []string{"start", "deployment"} {
		t.Run(entry, func(t *testing.T) {
			stubOperatorClient(t, nil)
			shortYoloctlHome(t)
			root, _ := layout.ConfigRoot()
			shared := filepath.Join(root, "shared")
			home := filepath.Join(shared, "lima")
			old := imageUpdateFixture(t)
			old.Name = "old"
			old.VM.Backend, old.VM.Image, old.VM.LimaHome = "lima", "", ""
			old.Mounts = []spaceconfig.Mount{{Source: shared}}
			if err := old.Save(); err != nil {
				t.Fatal(err)
			}
			fresh := *old
			fresh.Name = "new"
			fresh.DiskID = strings.Repeat("a", 32)
			fresh.Initializing, fresh.DiskUUID = true, ""
			fresh.VM.LimaHome = home
			fresh.Mounts = nil
			if err := fresh.Save(); err != nil {
				t.Fatal(err)
			}
			bootPath, _ := layout.SpaceConfigPath(fresh.Name)
			before, err := os.ReadFile(bootPath)
			if err != nil {
				t.Fatal(err)
			}
			calls := placementCalls(t, root)
			if entry == "start" {
				err = resourceCommand(t, "space", "start", fresh.Name)
			} else {
				err = startControllerSpaceImpl(t.Context(), &fresh, false, nil, io.Discard)
			}
			requirePlacementConflict(t, err, old.Name, shared)
			requireNoPlacementCalls(t, calls)
			if _, err := os.Stat(home); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("recovery created exposed storage: %v", err)
			}
			after, err := os.ReadFile(bootPath)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("recovery changed bootstrap: %v", err)
			}
		})
	}
}

func TestLimaPlacementAllowsAllocationBesideExistingGrant(t *testing.T) {
	stubOperatorClient(t, nil)
	shortYoloctlHome(t)
	root, _ := layout.ConfigRoot()
	home := filepath.Join(root, "new-home")
	grant := home + "-project"
	old := imageUpdateFixture(t)
	old.Name = "old"
	old.VM.Backend, old.VM.Image, old.VM.LimaHome = "lima", "", ""
	old.Mounts = []spaceconfig.Mount{{Source: grant, Writable: true}}
	if err := old.Save(); err != nil {
		t.Fatal(err)
	}
	fresh := *old
	fresh.Name, fresh.DiskID = "new", strings.Repeat("a", 32)
	fresh.Initializing, fresh.DiskUUID = true, ""
	fresh.VM.LimaHome, fresh.Mounts = home, nil
	if err := fresh.Save(); err != nil {
		t.Fatal(err)
	}
	calls := placementCalls(t, root)
	if err := vmhost.New().CreateAccountDisk(t.Context(), fresh.DiskID); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(calls)
	if err != nil || !strings.HasPrefix(string(raw), home+" disk create ") || strings.Count(string(raw), "\n") != 1 {
		t.Fatalf("unrelated share changed allocation: %q %v", raw, err)
	}
}
