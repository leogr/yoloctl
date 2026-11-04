package openshell

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func runtimeReservationFixture(t *testing.T, r *NativeReservation) (string, *os.File, runtimeShared) {
	t.Helper()
	populateReservationFixture(t, r)
	statusPath := filepath.Join(r.Directory(), "generations", "g"+strings.Repeat("d", 48), "status")
	status, err := os.Open(statusPath)
	if err != nil {
		t.Fatal(err)
	}
	shared, err := prepareRuntimeSharedAt(status, r.owners.supervisorUID, r.owners.supervisorGID)
	if err != nil {
		_ = status.Close()
		t.Fatal(err)
	}
	return statusPath, status, shared
}
func runtimeReservationSocket(t *testing.T, directory *os.File, name string, mode os.FileMode) {
	t.Helper()
	// A retained project-fixture directory FD keeps this path below Unix's
	// socket path bound without a privileged mount or namespace operation.
	name = fmt.Sprintf("/proc/self/fd/%d/%s", directory.Fd(), name)
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: name, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	if err := os.Chmod(name, mode); err != nil {
		_ = listener.Close()
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
}
func populateSupervisorRuntimeContents(t *testing.T, status *os.File, shared runtimeShared) {
	t.Helper()
	runtimeReservationSocket(t, shared.control, "control.sock", 0o666)
	runtimeReservationSocket(t, shared.supervisorRun, "ready.sock", 0o755) // inherited umask, no final chmod
	runtimeReservationSocket(t, shared.supervisorRun, "ssh/ssh.sock", 0o600)
	for name, mode := range map[string]os.FileMode{"proxy-tls/openshell-ca.pem": 0o600, "proxy-tls/ca-bundle.pem": 0o600, "proxy-tls/.tmpAb123Z": 0o600, "tmp/.tmp9a8B7c": 0o400} {
		path := fmt.Sprintf("/proc/self/fd/%d/%s", shared.supervisorRun.Fd(), name)
		if err := os.WriteFile(path, []byte("project TLS fixture"), mode); err != nil {
			t.Fatal(err)
		}
	}
	if err := status.Sync(); err != nil {
		t.Fatal(err)
	}
}
func TestOwnedReservationCleansActualRuntimeSharedInitialization(t *testing.T) {
	for _, contents := range []bool{false, true} {
		t.Run(fmt.Sprintf("contents%v", contents), func(t *testing.T) {
			r := ownedReservationFixture(t)
			_, status, shared := runtimeReservationFixture(t, r)
			if contents {
				populateSupervisorRuntimeContents(t, status, shared)
			}
			// PID1 closes handles, retaining these on-disk leaves after runtime stop.
			if err := shared.close(); err != nil {
				t.Fatal(err)
			}
			if err := status.Close(); err != nil {
				t.Fatal(err)
			}
			if err := r.RemoveAfterContainerRemoval(t.Context()); err != nil {
				t.Fatal("acknowledged runtime removal failed private cleanup", err)
			}
			if _, err := os.Lstat(r.Directory()); !os.IsNotExist(err) {
				t.Fatal("private state retained", err)
			}
		})
	}
}
func TestOwnedReservationCleansActualPartialRuntimeSharedInitialization(t *testing.T) {
	r := ownedReservationFixture(t)
	populateReservationFixture(t, r)
	statusPath := filepath.Join(r.Directory(), "generations", "g"+strings.Repeat("d", 48), "status")
	// The real initializer creates/chowns/chmods its first directory, then
	// fails parent fsync on this O_PATH descriptor. No production failure hook.
	fd, err := unix.Open(statusPath, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	status := os.NewFile(uintptr(fd), statusPath)
	shared, err := prepareRuntimeSharedAt(status, r.owners.supervisorUID, r.owners.supervisorGID)
	_ = shared.close()
	_ = status.Close()
	if !errors.Is(err, unix.EBADF) {
		t.Fatal("fixture did not reach real partial initialization", err)
	}
	if info, err := os.Stat(filepath.Join(statusPath, "runtime-control")); err != nil || info.Mode().Perm() != 0o711 {
		t.Fatal("partial initializer leaf missing", err)
	}
	if _, err := os.Lstat(filepath.Join(statusPath, "runtime-supervisor")); !os.IsNotExist(err) {
		t.Fatal("fixture unexpectedly completed", err)
	}
	if err := r.RemoveAfterContainerRemoval(t.Context()); err != nil {
		t.Fatal("owned partial shared state retained", err)
	}
}
func TestOwnedReservationRuntimeSchemaRefusesForeignLinksModesAndOwners(t *testing.T) {
	for _, change := range []string{"foreign", "control-mode", "tls-mode", "owner", "socket-link", "tls-link", "temp-name"} {
		t.Run(change, func(t *testing.T) {
			r := ownedReservationFixture(t)
			statusPath, status, shared := runtimeReservationFixture(t, r)
			populateSupervisorRuntimeContents(t, status, shared)
			_ = shared.close()
			_ = status.Close()
			control := filepath.Join(statusPath, "runtime-control")
			tls := filepath.Join(statusPath, "runtime-supervisor/proxy-tls")
			switch change {
			case "foreign":
				if err := os.WriteFile(filepath.Join(tls, "foreign"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			case "control-mode":
				if err := os.Chmod(control, 0o755); err != nil {
					t.Fatal(err)
				}
			case "tls-mode":
				if err := os.Chmod(filepath.Join(tls, "openshell-ca.pem"), 0o644); err != nil {
					t.Fatal(err)
				}
			case "owner":
				r.owners.supervisorUID++
			case "socket-link":
				if err := os.Remove(filepath.Join(control, "control.sock")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("/tmp", filepath.Join(control, "control.sock")); err != nil {
					t.Fatal(err)
				}
			case "tls-link":
				if err := os.Remove(filepath.Join(tls, "openshell-ca.pem")); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(filepath.Join(tls, "ca-bundle.pem"), filepath.Join(tls, "openshell-ca.pem")); err != nil {
					t.Fatal(err)
				}
			case "temp-name":
				if err := os.WriteFile(filepath.Join(tls, ".tmp-foreign"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := r.RemoveAfterContainerRemoval(t.Context()); err == nil {
				t.Fatal("foreign runtime state accepted")
			}
			if _, err := os.Stat(filepath.Join(r.Directory(), "origin.json")); err != nil {
				t.Fatal("schema refusal deleted origin", err)
			}
			if _, err := os.Stat(filepath.Join(tls, "ca-bundle.pem")); err != nil {
				t.Fatal("schema refusal partially deleted contents", err)
			}
		})
	}
}
