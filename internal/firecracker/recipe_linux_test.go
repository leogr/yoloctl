//go:build linux && amd64

package firecracker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/guestbinary"
	"github.com/leogr/yoloctl/internal/layout"
)

func TestRecipeIdentityTracksAllBuildInputs(t *testing.T) {
	base := strings.Repeat("a", 64)
	inputs := map[string]string{"pins": "one", "runtime": "two", "kernel": "three"}
	first, err := recipeIdentity(1, 4, base, "", "", inputs)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		recipe, protocol int
		init, url, sum   string
		inputs           map[string]string
	}{
		{2, 4, base, "", "", inputs}, {1, 5, base, "", "", inputs}, {1, 4, strings.Repeat("b", 64), "", "", inputs},
		{1, 4, base, "https://example.org/image", "checksum", inputs},
		{1, 4, base, "", "", map[string]string{"pins": "changed", "runtime": "two", "kernel": "three"}},
		{1, 4, base, "", "", map[string]string{"pins": "one", "runtime": "changed", "kernel": "three"}},
		{1, 4, base, "", "", map[string]string{"pins": "one", "runtime": "two", "kernel": "changed"}},
	} {
		next, err := recipeIdentity(tc.recipe, tc.protocol, tc.init, tc.url, tc.sum, tc.inputs)
		if err != nil || next == first {
			t.Fatalf("inputs reused old recipe: %s %v", next, err)
		}
	}
	if _, err := recipeIdentity(1, 4, "", "", "", inputs); err == nil {
		t.Fatal("unidentified init reused cache")
	}
}

func TestNewRecipeBuildsWithoutReplacingInstalledImages(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	source := fixtureImage(t, t.TempDir())
	builds := 0
	build := func(context.Context, string) (string, error) { builds++; return source, nil }
	first, err := resolveImage(t.Context(), "", testRecipe, build)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolveImage(t.Context(), "", strings.Repeat("b", 64), build); err != nil {
		t.Fatal(err)
	}
	if builds != 2 {
		t.Fatal("new recipe silently reused previous default")
	}
	if _, err := os.Stat(filepath.Join(first, "rootfs.ext4")); err != nil {
		t.Fatal("old installed image removed", err)
	}
	// Explicit overrides must not satisfy or overwrite the recipe cache.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, err := resolveImage(t.Context(), source, "", nil); err != nil {
		t.Fatal(err)
	}
	root, err := layout.VMImagesDir("firecracker")
	if err != nil {
		t.Fatal(err)
	}
	refs, err := filepath.Glob(filepath.Join(root, "default-*.json"))
	if err != nil || len(refs) != 0 {
		t.Fatalf("override published a default: %v %v", refs, err)
	}
}

func TestReadDefaultImageDoesNotPrepareOrCreateStorage(t *testing.T) {
	root := filepath.Join(t.TempDir(), "home")
	t.Setenv("YOLOCTL_HOME", root)
	old := guestbinary.InitAMD64SHA256
	guestbinary.InitAMD64SHA256 = strings.Repeat("a", 64)
	t.Cleanup(func() { guestbinary.InitAMD64SHA256 = old })
	if _, err := ReadDefaultImage(); err == nil {
		t.Fatal("missing cache accepted")
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("read prepared storage")
	}
	recipe, err := defaultRecipe()
	if err != nil {
		t.Fatal(err)
	}
	images, err := layout.VMImagesDir("firecracker")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(images, 0700); err != nil {
		t.Fatal(err)
	}
	ref := filepath.Join(images, "default-"+recipe+".json")
	if err := os.WriteFile(ref, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadDefaultImage(); err == nil {
		t.Fatal("corrupt cache accepted")
	}
	entries, err := os.ReadDir(images)
	if err != nil || len(entries) != 1 || entries[0].Name() != filepath.Base(ref) {
		t.Fatal("read mutated image cache", entries, err)
	}
}
