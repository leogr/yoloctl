//go:build linux

package agentbox

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"
)

func TestNativeRemovalOwnershipRenewsDetachedChecks(t *testing.T) {
	for _, hung := range []bool{false, true} {
		t.Run(fmt.Sprintf("hung-%t", hung), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				const delay = 80 * time.Second
				slow := func(ctx context.Context) error {
					select {
					case <-time.After(delay):
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				}
				checks := nativeRemovalOwnership{
					verifyStorage: slow,
					inspect: func(ctx context.Context) error {
						if hung {
							<-ctx.Done()
							return ctx.Err()
						}
						return slow(ctx)
					},
				}
				caller, cancel := context.WithTimeout(t.Context(), time.Minute)
				defer cancel()
				if err := checks.verify(caller); !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal("foreground ownership did not follow the caller", err)
				}
				checks.stopAcknowledged = true
				start := time.Now()
				err := checks.verify(context.WithoutCancel(t.Context()))
				if hung {
					if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) != delay+runtimeStepFailSafe {
						t.Fatalf("silent inspect lost its own bound: %v after %v", err, time.Since(start))
					}
				} else if err != nil || time.Since(start) != 2*delay {
					t.Fatalf("storage and inspect shared one wait: %v after %v", err, time.Since(start))
				}
			})
		})
	}
}

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
