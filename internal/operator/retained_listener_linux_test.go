package operator

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func retainedOperatorListenerFixture(t *testing.T) (string, Endpoint, *retainedOperatorListener) {
	t.Helper()
	// Keep the real socket path short, including on supported Unix hosts.
	root, err := os.MkdirTemp("", "op-listener-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	e := Endpoint{Box: "selected", Incarnation: strings.Repeat("a", 32)}
	dir := filepath.Join(root, e.Incarnation)
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	identity, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string][]byte{"identity.json": identity, "client.json": []byte(`{"space":"fixture","generation":"fixture"}`)} {
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(dir, name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p, err := newRetainedOperatorListener(func() (*net.UnixListener, error) {
		return net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(dir, "control.sock"), Net: "unix"})
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.listener.Close() })
	if err := os.Chmod(filepath.Join(dir, "control.sock"), 0o666); err != nil {
		t.Fatal(err)
	}
	return root, e, p
}

func TestRetainedOperatorListenerClosePreservesStrictWithdrawal(t *testing.T) {
	root, e, l := retainedOperatorListenerFixture(t)
	p, err := prepareEndpointRemovalAt(t.Context(), root, e, os.Geteuid(), os.Getegid(), endpointCurrentOK)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	original := p.members["control.sock"]
	if err := l.Close(); err != nil {
		t.Fatal("actual original listener Close failed", err)
	}
	current, err := endpointID(int(p.directory.Fd()), "control.sock")
	if err != nil || current != original {
		t.Fatal("listener Close changed the original socket member", err)
	}
	if err := p.check(t.Context(), endpointCurrentOK); err != nil {
		t.Fatal("strict original snapshot was refreshed/lost after listener Close", err)
	}
	var effects []string
	p.unlink = func(fd int, name string, flags int) error {
		effects = append(effects, "unlink:"+name)
		return unix.Unlinkat(fd, name, flags)
	}
	p.sync = func(fd int) error { effects = append(effects, "sync"); return unix.Fsync(fd) }
	if err := p.withdraw(t.Context(), endpointCurrentOK); err != nil {
		t.Fatal("actual Close and original filesystem withdrawal did not compose", err)
	}
	if want := "unlink:control.sock,sync,unlink:client.json,sync,unlink:identity.json,sync,unlink:" + e.Incarnation + ",sync"; strings.Join(effects, ",") != want {
		t.Fatal("Close replaced an original unlink/sync ACK", effects)
	}
	if err := l.Close(); err == nil {
		t.Fatal("completed listener Close became a new ACK")
	}
}

func TestRetainedOperatorListenerCloseIsNotAcceptedConnectionSettlement(t *testing.T) {
	root, e, l := retainedOperatorListenerFixture(t)
	accepted := make(chan net.Conn, 1)
	acceptErr := make(chan error, 1)
	go func() {
		c, err := l.accept()
		if err != nil {
			acceptErr <- err
			return
		}
		accepted <- c
	}()
	client, err := net.DialTimeout("unix", filepath.Join(root, e.Incarnation, "control.sock"), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	var peer net.Conn
	select {
	case peer = <-accepted:
	case err := <-acceptErr:
		t.Fatal(err)
	case <-time.After(3 * time.Second):
		t.Fatal("ordinary fixture Accept did not settle")
	}
	t.Cleanup(func() { _ = peer.Close() })
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if err := peer.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Write([]byte{'x'}); err != nil {
		t.Fatal(err)
	}
	var body [1]byte
	if _, err := peer.Read(body[:]); err != nil || body[0] != 'x' {
		t.Fatal("listener Close unexpectedly claimed accepted-stream retirement", err)
	}
	if c, err := l.accept(); err == nil || c != nil {
		t.Fatal("closed listener admitted a new Accept")
	}
}

func TestRetainedOperatorListenerCloseFailureStaysConsumed(t *testing.T) {
	_, _, l := retainedOperatorListenerFixture(t)
	boom := errors.New("failed original Close reply")
	calls := 0
	l.closeFD = func() error { calls++; return boom }
	if err := l.Close(); !errors.Is(err, boom) || calls != 1 {
		t.Fatal("original Close failure was lost", err)
	}
	if err := l.Close(); !errors.Is(err, boom) || calls != 1 {
		t.Fatal("failed original Close was retried or its cause lost", err, calls)
	}
}

func TestRetainedOperatorListenerPartialCreatorFailureCannotExposeOrUnlink(t *testing.T) {
	root, e, old := retainedOperatorListenerFixture(t)
	// The ordinary constructor never adopts a path already bound by another
	// creator. This does not qualify normal stale-socket restoration.
	name := filepath.Join(root, e.Incarnation, "control.sock")
	if got, err := newRetainedOperatorListener(func() (*net.UnixListener, error) {
		return net.ListenUnix("unix", &net.UnixAddr{Name: name, Net: "unix"})
	}); err == nil || got != nil {
		t.Fatal("another original listener/path was adopted")
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(name); err != nil {
		t.Fatal("failed other creator removed original socket", err)
	}
	boom := errors.New("partial creator reply failed")
	p, err := newRetainedOperatorListener(func() (*net.UnixListener, error) {
		l, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(root, "partial.sock"), Net: "unix"})
		if err != nil {
			return nil, err
		}
		return l, boom
	})
	if !errors.Is(err, boom) || p == nil {
		t.Fatal("partial creator failure was hidden", err)
	}
	t.Cleanup(func() { _ = p.listener.Close() })
	if c, err := p.accept(); !errors.Is(err, boom) || c != nil {
		t.Fatal("failed creator exposed its listener", err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root, "partial.sock")); err != nil {
		t.Fatal("partial owned Close unlinked without explicit withdrawal", err)
	}
}
