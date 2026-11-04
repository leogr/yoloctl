//go:build linux

package agentbox

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestNativeRemovalPublicSSHSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ssh.sock")
	if err := removeNativePublicSSHSocket(path); err != nil {
		t.Fatal(err)
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	defer func() { _ = listener.Close() }()
	if err := removeNativePublicSSHSocket(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("socket was not removed: %v", err)
	}
	if err := removeNativePublicSSHSocket(path); err != nil {
		t.Fatal(err)
	}
	// Later creation at the same name works without retained inode/mount proof.
	replacement, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = replacement.Close() }()
	if err := removeNativePublicSSHSocket(path); err != nil {
		t.Fatal(err)
	}
}

func TestNativeRemovalPublicSSHLeavesNonSocket(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"file", "symlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(dir, kind)
			var err error
			switch kind {
			case "file":
				err = os.WriteFile(path, []byte("retained"), 0600)
			case "symlink":
				err = os.Symlink(target, path)
			case "directory":
				err = os.Mkdir(path, 0700)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := removeNativePublicSSHSocket(path); err == nil {
				t.Fatal("unexpected endpoint type was not reported")
			}
			if _, err := os.Lstat(path); err != nil {
				t.Fatalf("unexpected endpoint was changed: %v", err)
			}
		})
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "retained" {
		t.Fatalf("symlink target changed: %q %v", data, err)
	}
}
