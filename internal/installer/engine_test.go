package installer

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/leogr/yoloctl/internal/releasebundle"
)

func fixtureBundle(t *testing.T, version, id string) string {
	t.Helper()
	dir := t.TempDir()
	source := filepath.Join(t.TempDir(), "fixture.go")
	if err := os.WriteFile(source, []byte("package main\nfunc main(){}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	const prefix = "github.com/leogr/yoloctl/internal/"
	flags := "-X " + prefix + "buildinfo.Version=" + version + " -X " + prefix + "controller.BuildID=" + id
	build := func(name, osname, arch, ldflags string) {
		t.Helper()
		cmd := exec.Command("go", "build", "-buildvcs=false", "-ldflags", ldflags, "-o", filepath.Join(dir, name), source)
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+osname, "GOARCH="+arch)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("fixture build: %s: %v", out, err)
		}
	}
	files := map[string]string{}
	for _, arch := range []string{"amd64", "arm64"} {
		name := "yoloctl-linux-" + arch
		build(name, "linux", arch, flags)
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		files[name] = fmt.Sprintf("%x", sha256.Sum256(raw))
	}
	hostFlags := flags + " -X " + prefix + "guestbinary.AMD64SHA256=" + files["yoloctl-linux-amd64"] + " -X " + prefix + "guestbinary.ARM64SHA256=" + files["yoloctl-linux-arm64"]
	build("yoloctl", runtime.GOOS, runtime.GOARCH, hostFlags)
	// Tests exercise publication on any executor, using the darwin payload set
	// when Linux amd64 would otherwise require an irrelevant PID 1 fixture.
	osname := runtime.GOOS
	if osname == "linux" && runtime.GOARCH == "amd64" {
		osname = "darwin"
	}
	names, err := releasebundle.Payload(osname, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"LICENSE", "INSTALL.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range names {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		files[name] = fmt.Sprintf("%x", sha256.Sum256(raw))
	}
	m := releasebundle.Manifest{Schema: 1, Version: version, Channel: "release", Bundle: id, OS: osname, Arch: runtime.GOARCH, Files: files}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(filepath.Join(dir, "MANIFEST.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := releasebundle.Verify(dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

func physicalTemp(t *testing.T) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return path
}
func TestPublishRetainsCompleteSelectionsAcrossFailures(t *testing.T) {
	first := fixtureBundle(t, "v1.0.0", strings.Repeat("a", 32))
	second := fixtureBundle(t, "v1.0.0", strings.Repeat("b", 32))
	for _, point := range []string{"none", "copy", "fsync", "rename", "link", "after-link"} {
		t.Run(point, func(t *testing.T) {
			root := physicalTemp(t)
			options := Options{Source: first, Store: filepath.Join(root, "store"), BinDir: filepath.Join(root, "bin")}
			old, err := Publish(t.Context(), options)
			if err != nil {
				t.Fatal(err)
			}
			before, err := Inspect(options.BinDir, options.Store)
			if err != nil {
				t.Fatal(err)
			}
			options.Source = second
			options.Expected = before
			options.fault = func(step string) error {
				if step == point {
					return errors.New("injected " + point)
				}
				return nil
			}
			next, err := Publish(t.Context(), options)
			if point == "none" && err != nil || point != "none" && err == nil {
				t.Fatalf("point %s: %v", point, err)
			}
			selected, err := filepath.EvalSymlinks(old.Link)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := releasebundle.Verify(filepath.Dir(selected)); err != nil {
				t.Fatalf("selected incomplete: %v", err)
			}
			if _, err := releasebundle.Verify(filepath.Dir(old.Executable)); err != nil {
				t.Fatalf("old bundle lost: %v", err)
			}
			if point == "none" && selected != next.Executable {
				t.Fatal("new selection not published")
			}
			if point == "after-link" && selected != next.Executable {
				t.Fatal("post-swap error hid complete new selection")
			}
			if point != "none" && point != "after-link" && selected != old.Executable {
				t.Fatal("failure changed prior selection")
			}
		})
	}
}

func TestPublishNoopCorruptionUnknownAndPathConflicts(t *testing.T) {
	source := fixtureBundle(t, "v2.0.0", strings.Repeat("c", 32))
	root := physicalTemp(t)
	o := Options{Source: source, Store: filepath.Join(root, "store"), BinDir: filepath.Join(root, "bin")}
	result, err := Publish(t.Context(), o)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Publish(t.Context(), o)
	if err != nil || !again.Already || again.Executable != result.Executable {
		t.Fatal(again, err)
	}
	if err := os.Chmod(result.Executable, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(result.Executable, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Publish(t.Context(), o); err == nil {
		t.Fatal("accepted corrupt managed install")
	}
	for _, kind := range []string{"unknown", "dangling", "symlink-store", "symlink-bin", "symlink-key"} {
		t.Run(kind, func(t *testing.T) {
			root := physicalTemp(t)
			o := Options{Source: source, Store: filepath.Join(root, "store"), BinDir: filepath.Join(root, "bin")}
			if err := os.Mkdir(o.BinDir, 0700); err != nil {
				t.Fatal(err)
			}
			other := physicalTemp(t)
			switch kind {
			case "unknown":
				marker := filepath.Join(root, "executed")
				script := []byte("#!/bin/sh\ntouch " + marker + "\n")
				if err := os.WriteFile(filepath.Join(o.BinDir, "yoloctl"), script, 0700); err != nil {
					t.Fatal(err)
				}
				e, err := Inspect(o.BinDir, o.Store)
				if err != nil || !e.Unknown {
					t.Fatal(e, err)
				}
				o.Expected = e
			case "dangling":
				if err := os.Symlink(filepath.Join(root, "absent"), filepath.Join(o.BinDir, "yoloctl")); err != nil {
					t.Fatal(err)
				}
			case "symlink-store":
				if err := os.Symlink(other, o.Store); err != nil {
					t.Fatal(err)
				}
			case "symlink-bin":
				if err := os.Rename(o.BinDir, o.BinDir+"-original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(other, o.BinDir); err != nil {
					t.Fatal(err)
				}
			case "symlink-key":
				m, err := releasebundle.Verify(source)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Join(o.Store, "bundles"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(other, filepath.Join(o.Store, "bundles", m.Key())); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := Publish(t.Context(), o); err == nil {
				t.Fatalf("accepted %s", kind)
			}
			entries, err := os.ReadDir(other)
			if err != nil || len(entries) != 0 {
				t.Fatalf("wrote through link: %v %v", entries, err)
			}
			if _, err := os.Stat(filepath.Join(root, "executed")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("unknown binary executed")
			}
		})
	}
}

func TestConcurrentPublicationAndDowngrade(t *testing.T) {
	source := fixtureBundle(t, "v2.0.0", strings.Repeat("d", 32))
	older := fixtureBundle(t, "v1.0.0", strings.Repeat("e", 32))
	root := physicalTemp(t)
	o := Options{Source: source, Store: filepath.Join(root, "store"), BinDir: filepath.Join(root, "bin")}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Go(func() { _, err := Publish(context.Background(), o); errs <- err })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	before, err := Inspect(o.BinDir, o.Store)
	if err != nil {
		t.Fatal(err)
	}
	o.Expected = before
	o.Source = older
	r, err := Publish(t.Context(), o)
	if err != nil || len(r.Retained) != 2 {
		t.Fatal(r, err)
	}
}

func TestExplicitUnknownReplacementRetainsOriginalWithoutExecutingIt(t *testing.T) {
	source := fixtureBundle(t, "v1.0.0", strings.Repeat("f", 32))
	root := physicalTemp(t)
	bin := filepath.Join(root, "bin")
	store := filepath.Join(root, "store")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	original := []byte("#!/bin/sh\nexit 99\n")
	if err := os.WriteFile(filepath.Join(bin, "yoloctl"), original, 0700); err != nil {
		t.Fatal(err)
	}
	before, err := Inspect(bin, store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Publish(t.Context(), Options{Source: source, Store: store, BinDir: bin, Expected: before, AllowUnknown: true}); err != nil {
		t.Fatal(err)
	}
	retained, err := os.ReadFile(filepath.Join(bin, ".yoloctl-unknown-"+before.Fingerprint))
	if err != nil || string(retained) != string(original) {
		t.Fatal("unknown original lost", err)
	}
}

// A home reached through a symlink (for example ~/.local -> elsewhere) is
// installed through its resolved path and recognized as managed afterwards.
func TestPublishThroughSymlinkedHomePrefix(t *testing.T) {
	root := physicalTemp(t)
	real := filepath.Join(root, "real")
	if err := os.MkdirAll(real, 0o700); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "home")
	if err := os.Symlink(real, home); err != nil {
		t.Fatal(err)
	}
	source := fixtureBundle(t, "v1.0.0", strings.Repeat("a", 32))
	store := filepath.Join(home, ".local", "share", "yoloctl")
	bin := filepath.Join(home, ".local", "bin")
	first, err := Publish(t.Context(), Options{Source: source, Store: store, BinDir: bin})
	if err != nil || first.Already || !strings.HasPrefix(first.Executable, real) {
		t.Fatal("symlinked home prefix refused or not resolved", first, err)
	}
	existing, err := Inspect(bin, store)
	if err != nil || existing.Unknown || existing.Manifest == nil {
		t.Fatal("published install through a symlinked prefix is not recognized as managed", existing, err)
	}
	again, err := Publish(t.Context(), Options{Source: source, Store: store, BinDir: bin, Expected: existing})
	if err != nil || !again.Already {
		t.Fatal("reinstall through the symlinked prefix was not a no-op", again, err)
	}
}
