//go:build linux && amd64

package firecracker

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/vmapi"
	"github.com/leogr/yoloctl/internal/vmwire"
)

const testRecipe = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func fixtureImage(t *testing.T, dir string) string {
	t.Helper()
	sum := fmt.Sprintf("%x", sha256.Sum256([]byte("image")))
	raw, _ := json.Marshal(Image{Schema: 1, Architecture: "amd64", GuestProtocol: vmwire.Protocol,
		KernelSHA256: sum, RootfsSHA256: sum, FirecrackerSHA256: sum})
	if err := os.WriteFile(filepath.Join(dir, "image.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"kernel", "rootfs.ext4", "firecracker"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("image"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestLocalPreparationIsLazySerializedAndReusable(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	source := fixtureImage(t, t.TempDir())
	var builds atomic.Int32
	prepare := func(context.Context, string) (string, error) {
		builds.Add(1)
		time.Sleep(20 * time.Millisecond)
		return source, nil
	}
	results := make(chan error, 4)
	for range 4 {
		go func() { _, err := resolveImage(t.Context(), "", testRecipe, prepare); results <- err }()
	}
	for range 4 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if builds.Load() != 1 {
		t.Fatalf("concurrent first use ran %d builds", builds.Load())
	}
	if _, err := resolveImage(t.Context(), "", testRecipe, func(context.Context, string) (string, error) {
		return "", errors.New("cached image must not require build tools")
	}); err != nil {
		t.Fatal(err)
	}
}

func TestFailedPreparationDoesNotPublishAndCanRetry(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, err := resolveImage(t.Context(), "", testRecipe, func(context.Context, string) (string, error) {
		return "", context.Canceled
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("failure hidden: %v", err)
	}
	source := fixtureImage(t, t.TempDir())
	if _, err := resolveImage(t.Context(), "", testRecipe, func(context.Context, string) (string, error) { return source, nil }); err != nil {
		t.Fatal(err)
	}
}

func TestImageWaitCancelsAndRejectsSymlinkLock(t *testing.T) {
	root := t.TempDir()
	unlock, err := imageLock(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if _, err := imageLock(ctx, root); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("uncancellable lock: %v", err)
	}
	other := t.TempDir()
	if err := os.Symlink(filepath.Join(root, ".prepare.lock"), filepath.Join(other, ".prepare.lock")); err != nil {
		t.Fatal(err)
	}
	if _, err := imageLock(t.Context(), other); err == nil {
		t.Fatal("symlink lock accepted")
	}
}

func TestInstalledImageSurvivesSourceRemovalAndRejectsChangedContent(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	source := t.TempDir()
	sum := fmt.Sprintf("%x", sha256.Sum256([]byte("image")))
	m := Image{Schema: 1, Architecture: "amd64", GuestProtocol: vmwire.Protocol, KernelSHA256: sum, RootfsSHA256: sum, FirecrackerSHA256: sum}
	raw, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(source, "image.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"kernel", "rootfs.ext4", "firecracker"} {
		if err := os.WriteFile(filepath.Join(source, name), []byte("image"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	installed, err := resolveImage(t.Context(), "", testRecipe, func(context.Context, string) (string, error) { return source, nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(source); err != nil {
		t.Fatal(err)
	}
	got, err := resolveImage(t.Context(), "", testRecipe, func(context.Context, string) (string, error) { t.Fatal("unexpected rebuild"); return "", nil })
	if err != nil || got != installed {
		t.Fatalf("cached selection %q %v", got, err)
	}
	if err := os.Chmod(filepath.Join(got, "kernel"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(got, "kernel"), []byte("other"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveImage(t.Context(), "", testRecipe, func(context.Context, string) (string, error) { t.Fatal("unexpected rebuild"); return "", nil }); err == nil {
		t.Fatal("changed cached image accepted")
	}
}

// Restoring an image directory from a copy changes inode identity even when
// bytes, modes and mtimes survive. Ordinary starts must reject the stale VM
// record; explicit image resolution must rehash before authorizing recreation.
func TestExplicitImageResolutionRechecksReplacedArtifacts(t *testing.T) {
	for _, name := range []string{"kernel", "rootfs.ext4", "firecracker"} {
		for _, content := range []string{"image", "other"} {
			t.Run(name+"/"+content, func(t *testing.T) {
				t.Setenv("XDG_CONFIG_HOME", t.TempDir())
				source := fixtureImage(t, t.TempDir())
				installed, err := resolveImage(t.Context(), source, "", nil)
				if err != nil {
					t.Fatal(err)
				}
				manifest, files, err := cachedImage(installed)
				if err != nil {
					t.Fatal(err)
				}
				old := Config{Image: manifest, ImageFiles: files, Spec: vmapi.Spec{Image: installed}}
				path := filepath.Join(installed, name)
				before, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				// Keep the original inode allocated until its replacement exists.
				replacement := path + ".restored"
				if err := os.WriteFile(replacement, []byte(content), before.Mode().Perm()); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(replacement, before.ModTime(), before.ModTime()); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(replacement, path); err != nil {
					t.Fatal(err)
				}
				if err := unchangedImage(old); err == nil || !strings.Contains(err.Error(), "artifact "+name+" changed") {
					t.Fatalf("stale VM record accepted or wrong failure: %v", err)
				}
				resolved, err := resolveImage(t.Context(), installed, "", nil)
				if content != "image" {
					if err == nil || !strings.Contains(err.Error(), name+" checksum mismatch") {
						t.Fatalf("corrupt restored image accepted or wrong failure: %v", err)
					}
					return
				}
				if err != nil || resolved != installed {
					t.Fatalf("unchanged content could not be reverified: %q %v", resolved, err)
				}
				verified, freshFiles, err := cachedImage(resolved)
				if err != nil {
					t.Fatal(err)
				}
				if verified != manifest || freshFiles[name] == files[name] {
					t.Fatal("reverification changed the manifest or retained the stale file identity")
				}
				if err := unchangedImage(Config{Image: verified, ImageFiles: freshFiles, Spec: vmapi.Spec{Image: resolved}}); err != nil {
					t.Fatalf("reverified image rejected: %v", err)
				}
				if err := unchangedImage(old); err == nil {
					t.Fatal("reverification silently authorized the old VM record")
				}
			})
		}
	}
}

func TestDefaultImageCannotSelectDifferentContentUnderOldIdentity(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	source := t.TempDir()
	sum := fmt.Sprintf("%x", sha256.Sum256([]byte("image")))
	m := Image{Schema: 1, Architecture: "amd64", GuestProtocol: vmwire.Protocol, KernelSHA256: sum, RootfsSHA256: sum, FirecrackerSHA256: sum}
	raw, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(source, "image.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"kernel", "rootfs.ext4", "firecracker"} {
		if err := os.WriteFile(filepath.Join(source, name), []byte("image"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	installed, err := resolveImage(t.Context(), "", testRecipe, func(context.Context, string) (string, error) { return source, nil })
	if err != nil {
		t.Fatal(err)
	}
	wrong := fmt.Sprintf("%064d", 1)
	root, err := layout.VMImagesDir("firecracker")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(installed, filepath.Join(root, wrong)); err != nil {
		t.Fatal(err)
	}
	ref, _ := json.Marshal(wrong)
	if err := os.WriteFile(filepath.Join(root, "default-"+testRecipe+".json"), ref, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveImage(t.Context(), "", testRecipe, func(context.Context, string) (string, error) { t.Fatal("unexpected rebuild"); return "", nil }); err == nil {
		t.Fatal("valid image accepted under mismatched content identity")
	}
}

func TestImageArchiveRejectsUnsafeMembers(t *testing.T) {
	for _, h := range []*tar.Header{{Name: "../kernel", Size: 1, Typeflag: tar.TypeReg}, {Name: "kernel", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}, {Name: "rootfs.ext4", Size: 9 << 30, Typeflag: tar.TypeReg}} {
		var b bytes.Buffer
		gz := gzip.NewWriter(&b)
		tw := tar.NewWriter(gz)
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		_ = tw.Close()
		_ = gz.Close()
		if err := unpackImage(&b, t.TempDir()); err == nil {
			t.Fatalf("accepted %+v", h)
		}
	}
}
