package boxcache

import (
	"os"
	"path/filepath"
	"testing"
)

func cacheFixture(t *testing.T) (string, int, int) {
	t.Helper()
	oldUID, oldGID := parentUID, parentGID
	parentUID, parentGID = os.Getuid(), os.Getgid()
	t.Cleanup(func() { parentUID, parentGID = oldUID, oldGID })
	uid, gid := os.Getuid(), os.Getgid()
	// Native qualification runs as root, but the space's workload owner is
	// always positive. Root can create the same private non-root leaf.
	if uid == 0 {
		uid = 1000
	}
	if gid == 0 {
		gid = 1000
	}
	return filepath.Join(t.TempDir(), "box-cache"), uid, gid
}

func TestCacheRetainsContentsAndRemovesOnlyItsOwnedLeaf(t *testing.T) {
	root, uid, gid := cacheFixture(t)
	for _, name := range []string{"one", "peer"} {
		if err := ensure(root, name, uid, gid); err != nil {
			t.Fatal(err)
		}
	}
	outside := filepath.Join(t.TempDir(), "keep")
	if err := os.WriteFile(outside, []byte("account/workdir"), 0o600); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "one", "cached")
	if err := os.WriteFile(file, []byte("cache"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(outside), filepath.Join(root, "one", "outside")); err != nil {
		t.Fatal(err)
	}
	if err := ensure(root, "one", uid, gid); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatal("ensure discarded a retained cache", err)
	}
	if err := remove(root, "one", uid, gid); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{outside, filepath.Join(root, "peer")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatal("removed unrelated state", err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "one")); !os.IsNotExist(err) {
		t.Fatal("leaf remains", err)
	}
	if err := remove(root, "one", uid, gid); err != nil {
		t.Fatal("missing leaf is harmless", err)
	}
}

func TestCacheRejectsForeignEntriesWithoutRepairOrDeletion(t *testing.T) {
	for _, kind := range []string{"link", "file", "mode", "owner", "parent-mode", "parent-link"} {
		t.Run(kind, func(t *testing.T) {
			root, uid, gid := cacheFixture(t)
			if err := ensure(root, "one", uid, gid); err != nil {
				t.Fatal(err)
			}
			leaf := filepath.Join(root, "one")
			marker := filepath.Join(t.TempDir(), "keep")
			if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "link", "file":
				if err := os.Remove(leaf); err != nil {
					t.Fatal(err)
				}
				if kind == "link" {
					if err := os.Symlink(filepath.Dir(marker), leaf); err != nil {
						t.Fatal(err)
					}
				} else if err := os.WriteFile(leaf, []byte("keep"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "mode":
				if err := os.Chmod(leaf, 0o755); err != nil {
					t.Fatal(err)
				}
			case "owner":
				uid++
			case "parent-mode":
				if err := os.Chmod(root, 0o777); err != nil {
					t.Fatal(err)
				}
			case "parent-link":
				moved := root + "-actual"
				if err := os.Rename(root, moved); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(moved, root); err != nil {
					t.Fatal(err)
				}
			}
			if err := ensure(root, "one", uid, gid); err == nil {
				t.Fatal("adopted foreign cache")
			}
			if err := remove(root, "one", uid, gid); err == nil {
				t.Fatal("removed foreign cache")
			}
			if _, err := os.Lstat(leaf); err != nil {
				t.Fatal("rejected entry was removed", err)
			}
			if _, err := os.Stat(marker); err != nil {
				t.Fatal("followed link", err)
			}
		})
	}
}

func TestCacheMissingAndInvalidNames(t *testing.T) {
	root, uid, gid := cacheFixture(t)
	if err := remove(root, "missing", uid, gid); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../peer", "/peer", "one/child", ""} {
		if err := ensure(root, name, uid, gid); err == nil {
			t.Fatal("accepted invalid name", name)
		}
		if err := remove(root, name, uid, gid); err == nil {
			t.Fatal("accepted invalid name", name)
		}
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("invalid names created state", err)
	}
}
