package installer

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/releasebundle"
	"golang.org/x/sys/unix"
)

// unsupported is a reply that both platforms classify as "no-replace flag
// unsupported"; Linux FUSE mounts actually reply EINVAL.
var unsupported = unix.EOPNOTSUPP

type renameCalls struct{ order []string }

func hookRenames(t *testing.T, first error, before func()) *renameCalls {
	t.Helper()
	calls := &renameCalls{}
	oldNoReplace, oldPlain := noReplaceRename, plainRename
	t.Cleanup(func() { noReplaceRename, plainRename = oldNoReplace, oldPlain })
	noReplaceRename = func(parent int, source, target string) error {
		calls.order = append(calls.order, "noreplace")
		if before != nil {
			before()
		}
		if first != nil {
			return first
		}
		return oldNoReplace(parent, source, target)
	}
	plainRename = func(parent int, source, target string) error {
		calls.order = append(calls.order, "plain")
		return oldPlain(parent, source, target)
	}
	return calls
}

func openParent(t *testing.T, dir string) int {
	t.Helper()
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Close(fd) })
	return fd
}

func TestPublishDirectoryFallsBackOnlyWhenNoReplaceIsUnsupported(t *testing.T) {
	if runtime.GOOS == "linux" && !noReplaceUnsupported(unix.EINVAL) {
		t.Fatal("Linux EINVAL (FUSE without RENAME_NOREPLACE) is not treated as unsupported")
	}
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "stage"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stage", "MANIFEST.json"), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := hookRenames(t, unsupported, nil)
	if err := publishDirectory(openParent(t, dir), "stage", "key"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(calls.order, ",") != "noreplace,plain" {
		t.Fatalf("rename order %v, want the no-replace attempt once, then the fallback", calls.order)
	}
	if raw, err := os.ReadFile(filepath.Join(dir, "key", "MANIFEST.json")); err != nil || string(raw) != "new" {
		t.Fatal("fallback did not publish the stage", err)
	}

	// Any other refusal is returned unchanged, without the fallback.
	if err := os.Mkdir(filepath.Join(dir, "stage2"), 0o700); err != nil {
		t.Fatal(err)
	}
	calls = hookRenames(t, unix.EACCES, nil)
	if err := publishDirectory(openParent(t, dir), "stage2", "key2"); !errors.Is(err, unix.EACCES) || strings.Join(calls.order, ",") != "noreplace" {
		t.Fatalf("other errno: %v, calls %v", err, calls.order)
	}
	// A lookup error other than ENOENT is returned, never a rename.
	if err := os.WriteFile(filepath.Join(dir, "file"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	calls = hookRenames(t, unsupported, nil)
	if err := publishDirectory(openParent(t, dir), "stage2", "file/key"); !errors.Is(err, unix.ENOTDIR) || strings.Join(calls.order, ",") != "noreplace" {
		t.Fatalf("lookup error: %v, calls %v", err, calls.order)
	}
}

// A target that appears at the publication boundary (after Publish's own key
// check) is refused by the fallback itself and left exactly as it was.
func TestPublishDirectoryFallbackNeverReplacesAPresentTarget(t *testing.T) {
	for _, kind := range []string{"bundle", "empty-dir", "symlink", "file"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Mkdir(filepath.Join(dir, "stage"), 0o700); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(dir, "key")
			create := func() {
				var err error
				switch kind {
				case "bundle":
					if err = os.Mkdir(target, 0o700); err == nil {
						err = os.WriteFile(filepath.Join(target, "MANIFEST.json"), []byte("old"), 0o600)
					}
				case "empty-dir":
					err = os.Mkdir(target, 0o700)
				case "symlink":
					err = os.Symlink(filepath.Join(dir, "elsewhere"), target)
				case "file":
					err = os.WriteFile(target, []byte("old"), 0o600)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			calls := hookRenames(t, unsupported, create)
			err := publishDirectory(openParent(t, dir), "stage", "key")
			if !errors.Is(err, unix.EEXIST) || strings.Join(calls.order, ",") != "noreplace" {
				t.Fatalf("present %s: %v, calls %v", kind, err, calls.order)
			}
			if _, err := os.Stat(filepath.Join(dir, "stage")); err != nil {
				t.Fatal("stage lost", err)
			}
			info, err := os.Lstat(target)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "bundle":
				if raw, err := os.ReadFile(filepath.Join(target, "MANIFEST.json")); err != nil || string(raw) != "old" {
					t.Fatal("existing bundle changed", err)
				}
			case "empty-dir":
				if entries, _ := os.ReadDir(target); !info.IsDir() || len(entries) != 0 {
					t.Fatal("empty directory changed")
				}
			case "symlink":
				if info.Mode()&os.ModeSymlink == 0 {
					t.Fatal("symlink replaced")
				}
			case "file":
				if raw, err := os.ReadFile(target); err != nil || string(raw) != "old" {
					t.Fatal("file changed", err)
				}
			}
		})
	}
}

// End to end: a store whose filesystem lacks the no-replace flag installs and
// selects the bundle; a collision at the boundary fails without replacement.
func TestPublishInstallsWhenNoReplaceIsUnsupported(t *testing.T) {
	source := fixtureBundle(t, "v1.0.0", strings.Repeat("a", 32))
	root := physicalTemp(t)
	o := Options{Source: source, Store: filepath.Join(root, "store"), BinDir: filepath.Join(root, "bin")}
	calls := hookRenames(t, unsupported, nil)
	result, err := Publish(t.Context(), o)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(calls.order, ",") != "noreplace,plain" {
		t.Fatalf("rename order %v", calls.order)
	}
	if selected, err := filepath.EvalSymlinks(result.Link); err != nil || selected != result.Executable {
		t.Fatal("link does not select the published bundle", selected, err)
	}

	other := fixtureBundle(t, "v1.0.1", strings.Repeat("b", 32))
	m, err := releasebundle.Verify(other)
	if err != nil {
		t.Fatal(err)
	}
	root = physicalTemp(t)
	o = Options{Source: other, Store: filepath.Join(root, "store"), BinDir: filepath.Join(root, "bin")}
	key := filepath.Join(o.Store, "bundles", m.Key())
	hookRenames(t, unsupported, func() {
		if err := os.Mkdir(key, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(key, "MANIFEST.json"), []byte("foreign"), 0o600); err != nil {
			t.Fatal(err)
		}
	})
	if _, err := Publish(t.Context(), o); !errors.Is(err, unix.EEXIST) || !strings.Contains(err.Error(), filepath.Join(o.Store, "bundles")) {
		t.Fatal("collision at the publication boundary was not refused with the folder named", err)
	}
	if raw, err := os.ReadFile(filepath.Join(key, "MANIFEST.json")); err != nil || string(raw) != "foreign" {
		t.Fatal("collision replaced the existing directory", err)
	}
}
