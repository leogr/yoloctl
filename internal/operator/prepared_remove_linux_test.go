package operator

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func preparedEndpointFixture(t *testing.T) (string, Endpoint, *PreparedEndpointRemoval) {
	t.Helper()
	root, e := endpointCleanupFixture(t)
	// Explicit roles avoid depending on the executor's umask.
	for path, mode := range map[string]os.FileMode{
		root: 0o700, filepath.Join(root, e.Incarnation): 0o755,
		filepath.Join(root, e.Incarnation, "identity.json"): 0o644,
		filepath.Join(root, e.Incarnation, "client.json"):   0o644,
	} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
	}
	// The retained directory path may exceed Unix's socket-path limit. Bind at
	// a short ordinary fixture path, then move the socket into its registration.
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(t.TempDir(), "s"), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	t.Cleanup(func() { _ = listener.Close() })
	socket := filepath.Join(root, e.Incarnation, "control.sock")
	if err := os.Rename(listener.Addr().String(), socket); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(socket, 0o666); err != nil {
		t.Fatal(err)
	}
	p, err := prepareEndpointRemovalAt(t.Context(), root, e, os.Geteuid(), os.Getegid(), endpointCurrentOK)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return root, e, p
}

func endpointCurrentOK(context.Context) error { return nil }

func TestPreparedEndpointRemovalOriginalObservationAndOrderedEffects(t *testing.T) {
	root, e, p := preparedEndpointFixture(t)
	other := filepath.Join(root, strings.Repeat("b", 32))
	if err := os.Mkdir(other, 0o755); err != nil {
		t.Fatal(err)
	}
	var effects []string
	p.unlink = func(fd int, name string, flags int) error {
		effects = append(effects, "unlink:"+name)
		return unix.Unlinkat(fd, name, flags)
	}
	p.sync = func(fd int) error {
		effects = append(effects, "sync")
		return unix.Fsync(fd)
	}
	if err := p.withdraw(t.Context(), endpointCurrentOK); err != nil {
		t.Fatal(err)
	}
	want := "unlink:control.sock,sync,unlink:client.json,sync,unlink:identity.json,sync,unlink:" + e.Incarnation + ",sync"
	if strings.Join(effects, ",") != want {
		t.Fatalf("wrong acknowledgement order: %v", effects)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatal("other incarnation affected", err)
	}
	if _, err := os.Lstat(filepath.Join(root, e.Incarnation)); !os.IsNotExist(err) {
		t.Fatal("original registration retained", err)
	}
	before := len(effects)
	if err := p.withdraw(t.Context(), endpointCurrentOK); err == nil || len(effects) != before {
		t.Fatal("completed withdrawal was replayed")
	}
}

func TestPreparedEndpointRemovalRefusesChangedOriginal(t *testing.T) {
	for _, change := range []string{"parent", "directory", "client-replaced", "client-rewritten", "socket-gone", "extra", "symlink", "hardlink", "mode", "identity"} {
		t.Run(change, func(t *testing.T) {
			root, e, p := preparedEndpointFixture(t)
			dir := filepath.Join(root, e.Incarnation)
			client := filepath.Join(dir, "client.json")
			var err error
			switch change {
			case "parent":
				err = os.Rename(root, root+"-old")
				if err == nil {
					t.Cleanup(func() { _ = os.Rename(root+"-old", root) })
					err = os.Mkdir(root, 0o700)
					t.Cleanup(func() { _ = os.Remove(root) })
				}
			case "directory":
				err = os.Rename(dir, dir+"-old")
				if err == nil {
					err = os.Mkdir(dir, 0o755)
				}
			case "client-replaced":
				raw := append([]byte(nil), p.raw["client.json"]...)
				err = os.Rename(client, client+"-old")
				if err == nil {
					err = os.WriteFile(client, raw, 0o644)
				}
				if err == nil {
					err = os.Remove(client + "-old")
				}
			case "client-rewritten":
				// Same inode and length, different nonsecret routing bytes.
				raw := append([]byte(nil), p.raw["client.json"]...)
				raw[10] ^= 1
				err = os.WriteFile(client, raw, 0o644)
			case "socket-gone":
				err = os.Remove(filepath.Join(dir, "control.sock"))
			case "extra":
				err = os.WriteFile(filepath.Join(dir, "foreign"), nil, 0o644)
			case "symlink":
				err = os.Remove(client)
				if err == nil {
					err = os.Symlink("identity.json", client)
				}
			case "hardlink":
				err = os.Link(client, filepath.Join(root, "alias"))
			case "mode":
				err = os.Chmod(client, 0o666)
			case "identity":
				err = os.WriteFile(filepath.Join(dir, "identity.json"), []byte(`{}`), 0o644)
			}
			if err != nil {
				t.Fatal(err)
			}
			effects := 0
			p.unlink = func(int, string, int) error { effects++; return nil }
			if err := p.withdraw(t.Context(), endpointCurrentOK); err == nil || effects != 0 {
				t.Fatal("changed original reached unlink", err, effects)
			}
		})
	}
}

func TestPreparedEndpointRemovalPreservesFailedAcknowledgements(t *testing.T) {
	for _, fault := range []string{"current", "unlink-socket", "sync-socket", "unlink-client", "sync-client", "unlink-identity", "sync-identity", "rmdir", "sync-parent"} {
		t.Run(fault, func(t *testing.T) {
			_, e, p := preparedEndpointFixture(t)
			boom := errors.New("actual operation failed")
			last := ""
			calls := 0
			p.unlink = func(fd int, name string, flags int) error {
				calls++
				last = name
				if fault == "unlink-socket" && name == "control.sock" || fault == "unlink-client" && name == "client.json" || fault == "unlink-identity" && name == "identity.json" || fault == "rmdir" && name == e.Incarnation {
					return boom
				}
				return unix.Unlinkat(fd, name, flags)
			}
			p.sync = func(fd int) error {
				calls++
				if fault == "sync-socket" && last == "control.sock" || fault == "sync-client" && last == "client.json" || fault == "sync-identity" && last == "identity.json" || fault == "sync-parent" && last == e.Incarnation {
					return boom
				}
				return unix.Fsync(fd)
			}
			current := func(context.Context) error {
				if fault == "current" {
					return boom
				}
				return nil
			}
			if err := p.withdraw(t.Context(), current); !errors.Is(err, boom) {
				t.Fatal("actual error lost", err)
			}
			before := calls
			if err := p.withdraw(t.Context(), endpointCurrentOK); err == nil || calls != before {
				t.Fatal("failed or lost acknowledgement retried", err)
			}
			switch fault {
			case "sync-parent":
				if len(p.members) != 0 {
					t.Fatal("prior acknowledged file effects lost")
				}
			case "sync-socket":
				if _, exists := p.members["control.sock"]; !exists {
					t.Fatal("visible unlink substituted for sync acknowledgement")
				}
			}
		})
	}
}

func TestPreparedEndpointRemovalRechecksBetweenEffects(t *testing.T) {
	root, e, p := preparedEndpointFixture(t)
	checks := 0
	err := p.withdraw(t.Context(), func(context.Context) error {
		checks++
		if checks == 2 {
			return os.WriteFile(filepath.Join(root, e.Incarnation, "foreign"), nil, 0o644)
		}
		return nil
	})
	if err == nil || checks != 2 {
		t.Fatal("late changed inventory accepted", err, checks)
	}
	for _, name := range []string{"client.json", "identity.json", "foreign"} {
		if _, err := os.Stat(filepath.Join(root, e.Incarnation, name)); err != nil {
			t.Fatal("late refusal removed original anchors or foreign file", err)
		}
	}
}

func TestPreparedEndpointRemovalBoundaries(t *testing.T) {
	root, e, p := preparedEndpointFixture(t)
	if _, err := PrepareEndpointRemoval(t.Context(), e, endpointCurrentOK); err == nil {
		t.Fatal("ordinary executor constructed native withdrawal")
	}
	if err := p.Withdraw(t.Context(), endpointCurrentOK); err == nil || p.attempted {
		t.Fatal("ordinary executor reached native withdrawal")
	}
	for _, verifier := range []func(context.Context) error{nil, func(context.Context) error { return errors.New("unavailable") }} {
		if got, err := prepareEndpointRemovalAt(t.Context(), root, e, os.Geteuid(), os.Getegid(), verifier); err == nil || got != nil {
			t.Fatal("missing or failed actual owner accepted")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if got, err := prepareEndpointRemovalAt(ctx, root, e, os.Geteuid(), os.Getegid(), endpointCurrentOK); !errors.Is(err, context.Canceled) || got != nil {
		t.Fatal("cancelled observation accepted", err)
	}
	if got := fmt.Sprintf("%v %#v", p, p); strings.Contains(got, e.Incarnation) || strings.Contains(got, root) {
		t.Fatal("formatter exposed observation")
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if err := p.withdraw(t.Context(), endpointCurrentOK); err == nil {
		t.Fatal("closed observation reused")
	}
	var empty *PreparedEndpointRemoval
	if err := empty.withdraw(t.Context(), endpointCurrentOK); err == nil {
		t.Fatal("nil observation reused")
	}
}
