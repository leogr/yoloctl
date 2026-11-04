package lima

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstanceSocketPathByteBoundaries(t *testing.T) {
	for _, limit := range []int{104, 108} {
		for _, size := range []int{limit - 1, limit} {
			t.Run(fmt.Sprintf("limit-%d/bytes-%d", limit, size), func(t *testing.T) {
				const home = "/configuration/lima"
				name := strings.Repeat("a", size-len(filepath.Join(home, InstanceName(""), longestSocketName)))
				err := validateInstanceSocketPath(name, home, limit)
				if (err != nil) != (size >= limit) {
					t.Fatalf("%d-byte socket, limit %d: %v", size, limit, err)
				}
				if err != nil {
					for _, want := range []string{name, filepath.Join(home, InstanceName(name), longestSocketName), fmt.Sprintf("%d bytes", size), fmt.Sprintf("less than %d bytes", limit), "shorter space name", "YOLOCTL_HOME for a new space"} {
						if !strings.Contains(err.Error(), want) {
							t.Errorf("error missing %q: %v", want, err)
						}
					}
				}
			})
		}
	}
}

func TestInstanceSocketPathUsesHomeAndUTF8Bytes(t *testing.T) {
	if err := validateInstanceSocketPath(strings.Repeat("a", 63), "/l", 104); err != nil {
		t.Fatal("valid 63-byte name refused in a fitting home", err)
	}
	name := strings.Repeat("a", 40)
	if err := validateInstanceSocketPath(name, "/l", 104); err != nil {
		t.Fatal(err)
	}
	if err := validateInstanceSocketPath(name, "/longer/configuration/home/for/lima", 104); err == nil {
		t.Fatal("ignored selected home")
	}
	home := "/" + strings.Repeat("é", 36)
	socket := filepath.Join(home, InstanceName("a"), longestSocketName)
	if len(socket) != 106 {
		t.Fatal("incorrect UTF-8 fixture", len(socket))
	}
	if err := validateInstanceSocketPath("a", home, 104); err == nil {
		t.Fatal("counted characters instead of bytes")
	}
	if err := validateInstanceSocketPath("a", home, 108); err != nil {
		t.Fatal(err)
	}
}

// These are directories and fake executables, never bound Unix sockets. A
// short owned root also keeps the fitting case portable to macOS's temp setup.
func socketTestRoot(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "yl-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	return root
}

func TestValidateInstancePathsResolvesExistingHome(t *testing.T) {
	for _, longTarget := range []bool{true, false} {
		t.Run(fmt.Sprintf("long-target-%t", longTarget), func(t *testing.T) {
			base := socketTestRoot(t)
			short := filepath.Join(base, "short")
			long := filepath.Join(base, strings.Repeat("x", 100))
			root, target := short, long
			if !longTarget {
				root, target = long, short
			}
			for _, path := range []string{root, target} {
				if err := os.MkdirAll(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(target, filepath.Join(root, "lima")); err != nil {
				t.Fatal(err)
			}
			t.Setenv("YOLOCTL_HOME", root)
			err := ValidateInstancePaths("a")
			if (err != nil) != longTarget {
				t.Fatalf("resolved home %s: %v", target, err)
			}
			if longTarget && !strings.Contains(err.Error(), target) {
				t.Fatal("error did not identify resolved path", err)
			}
		})
	}
}

func TestValidateInstancePathsAbsentHomeHasNoEffects(t *testing.T) {
	base := socketTestRoot(t)
	root := filepath.Join(base, strings.Repeat("x", 100), "absent")
	t.Setenv("YOLOCTL_HOME", root)
	t.Setenv("LIMA_HOME", base) // Startup selects layout.LimaHome, not an inherited home.
	if err := ValidateInstancePaths("a"); err == nil {
		t.Fatal("accepted long selected home")
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("validation created home", err)
	}
	t.Setenv("YOLOCTL_HOME", filepath.Join(base, "new"))
	if err := ValidateInstancePaths("a"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(base, "new")); !os.IsNotExist(err) {
		t.Fatal("validation created fitting home", err)
	}
}

func TestLimaBootPreflightLeavesCleanupAndReadsAvailable(t *testing.T) {
	base := socketTestRoot(t)
	calls := filepath.Join(base, "calls")
	t.Setenv("LIMA_TEST_CALLS", calls)
	binary := filepath.Join(base, "limactl")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s\\n' \"$1\" >> \"$LIMA_TEST_CALLS\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	c := &Client{binary: binary, stdout: io.Discard, stderr: io.Discard}
	t.Setenv("YOLOCTL_HOME", filepath.Join(base, strings.Repeat("x", 100)))
	for _, err := range []error{c.Create(t.Context(), "a", nil), c.Start(t.Context(), "a")} {
		if err == nil || !strings.Contains(err.Error(), "Lima socket path") {
			t.Fatal("boot reached limactl", err)
		}
	}
	if _, err := os.Stat(calls); !os.IsNotExist(err) {
		t.Fatal("invalid boot executed limactl", err)
	}
	if err := c.Stop(t.Context(), "a"); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(t.Context(), "a", true); err != nil {
		t.Fatal(err)
	}
	if _, err := c.List(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := InstanceDir("a"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("YOLOCTL_HOME", base)
	if err := c.Create(t.Context(), "a", nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Start(t.Context(), "a"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(calls)
	if err != nil || string(data) != "stop\ndelete\nlist\ncreate\nstart\n" {
		t.Fatalf("unexpected backend calls %q: %v", data, err)
	}
}
