package agentbox

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// endpointFixture is a socket directory with an ssh.sock; the production
// checks of its owner (root) are exercised only when the test runs as root.
func endpointFixture(t *testing.T) (string, func() (*os.File, error)) {
	t.Helper()
	dir, err := os.MkdirTemp("", "ep") // short: a Unix socket path must fit macOS too
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	listener, err := net.Listen("unix", filepath.Join(dir, "ssh.sock"))
	if err != nil {
		t.Fatal(err)
	}
	listener.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = listener.Close()
	if err := os.Chmod(filepath.Join(dir, "ssh.sock"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, func() (*os.File, error) {
		fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return nil, err
		}
		return os.NewFile(uintptr(fd), dir), nil
	}
}

func answer(v bool, err error) func(context.Context) (bool, error) {
	return func(context.Context) (bool, error) { return v, err }
}

func socketPresent(t *testing.T, dir string) bool {
	t.Helper()
	_, err := os.Lstat(filepath.Join(dir, "ssh.sock"))
	return err == nil
}

func TestStoppedEndpointIsKeptUnlessEveryCheckHolds(t *testing.T) {
	for label, c := range map[string]struct {
		running, listed func(context.Context) (bool, error)
		want            string
	}{
		"container running":     {answer(true, nil), answer(false, nil), "container is running"},
		"container unreadable":  {answer(false, errors.New("engine down")), answer(false, nil), "could not be read"},
		"forward still listed":  {answer(false, nil), answer(true, nil), "forward of the box is still listed"},
		"forwards not listable": {answer(false, nil), answer(false, errors.New("systemd down")), "could not be listed"},
	} {
		dir, open := endpointFixture(t)
		err := removeStoppedEndpoint(t.Context(), open, c.running, c.listed)
		if err == nil || !strings.Contains(err.Error(), c.want) || !socketPresent(t, dir) {
			t.Fatalf("%s: %v (socket present %v)", label, err, socketPresent(t, dir))
		}
	}
	// The directory was replaced between the checks: kept.
	dir, open := endpointFixture(t)
	opened := 0
	other := t.TempDir()
	replaced := func() (*os.File, error) {
		if opened++; opened == 1 {
			return open()
		}
		return os.Open(other)
	}
	if err := removeStoppedEndpoint(t.Context(), replaced, answer(false, nil), answer(false, nil)); err == nil ||
		!strings.Contains(err.Error(), "directory was replaced") || !socketPresent(t, dir) {
		t.Fatalf("replaced: %v", err)
	}
	// Nothing to remove: no directory, or no socket in it.
	if err := removeStoppedEndpoint(t.Context(), func() (*os.File, error) { return nil, unix.ENOENT }, nil, nil); err != nil {
		t.Fatalf("no directory: %v", err)
	}
	dir, open = endpointFixture(t)
	if err := os.Remove(filepath.Join(dir, "ssh.sock")); err != nil {
		t.Fatal(err)
	}
	if err := removeStoppedEndpoint(t.Context(), open, nil, nil); err != nil {
		t.Fatalf("no socket: %v", err)
	}
}

func TestStoppedEndpointIsRemovedOnlyAsTheProtectedSocket(t *testing.T) {
	dir, open := endpointFixture(t)
	err := removeStoppedEndpoint(t.Context(), open, answer(false, nil), answer(false, nil))
	if os.Geteuid() != 0 {
		// Not owned by root: not the protected runtime socket, so kept.
		if err == nil || !strings.Contains(err.Error(), "protected runtime SSH socket") || !socketPresent(t, dir) {
			t.Fatalf("unprotected socket: %v", err)
		}
		return
	}
	if err != nil || socketPresent(t, dir) {
		t.Fatalf("stopped box: %v (socket present %v)", err, socketPresent(t, dir))
	}
}
