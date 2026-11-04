package agentbox

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestNativeGenerationStartActualEntryRequiresAdmissionBeforeAnyEffect(t *testing.T) {
	if err := (LiveApplier{}).startNativeStoppedGeneration(t.Context(), &Config{}, nil); err == nil {
		t.Fatal("ordinary execution entered native generation Start")
	}
	if _, err := ensureNativeGenerationSocketDirectory("fixture"); err == nil {
		t.Fatal("ordinary execution created native SSH directories")
	}
}
func TestNativeGenerationSocketPreparationUsesOwnedDescriptorParentsAndRetainsEndpoint(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "run"), 0o755); err != nil {
		t.Fatal(err)
	}
	anchor, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = anchor.Close() }()
	dir, err := ensureNativeGenerationSocketAt(anchor, []string{"run", "yoloctl", "fixture"}, uint32(os.Geteuid()), uint32(os.Getegid()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dir.Close() }()
	if err := requireEmptyNativeGenerationSocket(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "run", "yoloctl", "fixture", "ssh.sock")
	if err := os.WriteFile(path, []byte("retained foreign endpoint"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := requireEmptyNativeGenerationSocket(dir); err == nil {
		t.Fatal("stale/foreign endpoint admitted Start")
	}
	if raw, err := os.ReadFile(path); err != nil || string(raw) != "retained foreign endpoint" {
		t.Fatal("Start preparation removed/repaired old endpoint", err)
	}
	again, err := ensureNativeGenerationSocketAt(anchor, []string{"run", "yoloctl", "fixture"}, uint32(os.Geteuid()), uint32(os.Getegid()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = again.Close() }()
	before, _ := dir.Stat()
	after, _ := again.Stat()
	if !os.SameFile(before, after) {
		t.Fatal("existing protected directory was replaced")
	}
}
func TestNativeGenerationSocketPreparationRefusesLinksModesMissingParentAndInvalidComponents(t *testing.T) {
	for _, change := range []string{"missing-run", "run-link", "parent-link", "box-link", "writable-parent", "file-parent", "invalid-component", "wrong-owner"} {
		t.Run(change, func(t *testing.T) {
			root, outside := t.TempDir(), t.TempDir()
			run := filepath.Join(root, "run")
			if change != "missing-run" {
				if err := os.Mkdir(run, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			components := []string{"run", "yoloctl", "fixture"}
			uid, gid := uint32(os.Geteuid()), uint32(os.Getegid())
			switch change {
			case "run-link":
				if err := os.Remove(run); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, run); err != nil {
					t.Fatal(err)
				}
			case "parent-link":
				if err := os.Symlink(outside, filepath.Join(run, "yoloctl")); err != nil {
					t.Fatal(err)
				}
			case "box-link":
				if err := os.Mkdir(filepath.Join(run, "yoloctl"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Join(run, "yoloctl", "fixture")); err != nil {
					t.Fatal(err)
				}
			case "writable-parent":
				if err := os.Chmod(run, 0o777); err != nil {
					t.Fatal(err)
				}
			case "file-parent":
				if err := os.WriteFile(filepath.Join(run, "yoloctl"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			case "invalid-component":
				components[2] = ".."
			case "wrong-owner":
				uid++
			}
			anchor, err := os.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = anchor.Close() }()
			if dir, err := ensureNativeGenerationSocketAt(anchor, components, uid, gid); err == nil {
				if dir != nil {
					_ = dir.Close()
				}
				t.Fatal("unsafe SSH parent/components accepted", change)
			}
			entries, err := os.ReadDir(outside)
			if err != nil || len(entries) != 0 {
				t.Fatal("descriptor walk followed a link into foreign directory", err, entries)
			}
			if change == "invalid-component" {
				if _, err := os.Lstat(filepath.Join(run, "yoloctl")); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("invalid late component caused an earlier mkdir", err)
				}
			}
		})
	}
	if _, err := ensureNativeGenerationSocketAt(nil, nil, 0, 0); err == nil {
		t.Fatal("missing trusted parent accepted")
	}
	if err := requireEmptyNativeGenerationSocket(nil); err == nil {
		t.Fatal("missing SSH directory accepted")
	}
	// No Unix mount/network syscall probe occurs; only ordinary owned files.
}
