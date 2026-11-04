package installer

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/releasebundle"
)

func copyPathFixture(t *testing.T, from, to string) {
	t.Helper()
	if err := os.Mkdir(to, 0700); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(from)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(from, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(to, entry.Name()), data, info.Mode().Perm()); err != nil {
			t.Fatal(err)
		}
	}
}
func pathTreeSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	snapshot := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		value := info.Mode().String()
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			value += " " + target
		} else if info.Mode().IsRegular() {
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			value += fmt.Sprintf(" %x", sha256.Sum256(raw))
		}
		snapshot[rel] = value
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestRejectBinOverlapPreservesBundles(t *testing.T) {
	first := fixtureBundle(t, "v1.0.0", strings.Repeat("1", 32))
	second := fixtureBundle(t, "v2.0.0", strings.Repeat("2", 32))
	for _, scenario := range []string{"current-key", "retained-key", "store", "store-descendant", "source", "source-descendant", "ancestor", "alias-current-key", "alias-source", "alias-missing-store-descendant"} {
		t.Run(scenario, func(t *testing.T) {
			root := physicalTemp(t)
			source, source2 := filepath.Join(root, "source"), filepath.Join(root, "source2")
			copyPathFixture(t, first, source)
			copyPathFixture(t, second, source2)
			store, bin := filepath.Join(root, "store"), filepath.Join(root, "bin")
			old, err := Publish(t.Context(), Options{Source: source, Store: store, BinDir: bin})
			if err != nil {
				t.Fatal(err)
			}
			current, err := Inspect(bin, store)
			if err != nil {
				t.Fatal(err)
			}
			newer, err := Publish(t.Context(), Options{Source: source2, Store: store, BinDir: bin, Expected: current})
			if err != nil {
				t.Fatal(err)
			}
			var destination string
			switch scenario {
			case "current-key":
				destination = filepath.Dir(newer.Executable)
			case "retained-key":
				destination = filepath.Dir(old.Executable)
			case "store":
				destination = store
			case "store-descendant":
				destination = filepath.Join(store, "not-created", "bin")
			case "source":
				destination = source2
			case "source-descendant":
				destination = filepath.Join(source2, "not-created", "bin")
			case "ancestor":
				destination = root
			case "alias-current-key", "alias-source", "alias-missing-store-descendant":
				target := filepath.Dir(newer.Executable)
				if scenario == "alias-source" {
					target = source2
				}
				if scenario == "alias-missing-store-descendant" {
					target = store
				}
				alias := filepath.Join(root, "alias")
				if err := os.Symlink(target, alias); err != nil {
					t.Fatal(err)
				}
				destination = alias
				if scenario == "alias-missing-store-descendant" {
					destination = filepath.Join(alias, "not-created", "bin")
				}
			}
			// Model explicit unknown-file approval; it must never waive the immutable
			// source/store boundary. Capture after all fixture setup and before Publish.
			expected, err := Inspect(destination, store)
			if err != nil && !strings.HasPrefix(scenario, "alias-") {
				t.Fatal(err)
			}
			before := pathTreeSnapshot(t, root)
			if _, err := Publish(t.Context(), Options{Source: source2, Store: store, BinDir: destination, Expected: expected, AllowUnknown: true}); err == nil || !strings.Contains(err.Error(), "overlap") {
				t.Fatal("accepted overlapping bin directory", destination)
			}
			if after := pathTreeSnapshot(t, root); !reflect.DeepEqual(before, after) {
				t.Fatal("refusal mutated source, store, selection or destination")
			}
			selected, err := filepath.EvalSymlinks(newer.Link)
			if err != nil || selected != newer.Executable {
				t.Fatal("prior selection no longer usable", selected, err)
			}
			for _, executable := range []string{old.Executable, newer.Executable} {
				if _, err := releasebundle.Verify(filepath.Dir(executable)); err != nil {
					t.Fatal("retained bundle corrupted", err)
				}
			}
		})
	}
}

func TestOverlapRefusalDoesNotCreateStoreOrDestination(t *testing.T) {
	root := physicalTemp(t)
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	// Deliberately no manifest: path refusal is the first operation and must not
	// be masked by source validation or create either missing tree.
	store := filepath.Join(root, "new-store")
	before := pathTreeSnapshot(t, root)
	_, err := Publish(t.Context(), Options{Source: source, Store: store, BinDir: filepath.Join(store, "bin")})
	if err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatal(err)
	}
	if after := pathTreeSnapshot(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("overlap preflight wrote storage")
	}
}

func TestInstalledSourceAndSelectionLinkRemainSupported(t *testing.T) {
	source := fixtureBundle(t, "v1.0.0", strings.Repeat("3", 32))
	root := physicalTemp(t)
	store, bin := filepath.Join(root, "store"), filepath.Join(root, "bin")
	result, err := Publish(t.Context(), Options{Source: source, Store: store, BinDir: bin})
	if err != nil {
		t.Fatal(err)
	}
	current, err := Inspect(bin, store)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Publish(t.Context(), Options{Source: filepath.Dir(result.Executable), Store: store, BinDir: bin, Expected: current})
	if err != nil || !again.Already || again.Executable != result.Executable {
		t.Fatal("valid managed selection rejected", again, err)
	}
}
