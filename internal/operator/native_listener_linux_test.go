package operator

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func nativeListenerFixture(t *testing.T) (*NativeListener, string) {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "opnative-")
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(root, "control.sock")
	identity := RegistrationIdentity{Endpoint: Endpoint{Box: "dev", Incarnation: strings.Repeat("a", 32)}, Mode: RegistrationOpenShellNative, HelperProtocol: 2, Qualification: strings.Repeat("b", 64)}
	l, err := NewNativeListener(identity, func() (*net.UnixListener, error) {
		return net.ListenUnix("unix", &net.UnixAddr{Name: name, Net: "unix"})
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = l.service.listener.listener.Close() // test-owned fixture cleanup only
		_ = os.RemoveAll(root)
	})
	return l, name
}

func TestNativeListenerRequiresNativeIdentity(t *testing.T) {
	legacy := LegacyRegistrationIdentity(Endpoint{Box: "dev", Incarnation: strings.Repeat("a", 32)})
	if _, err := NewNativeListener(legacy, func() (*net.UnixListener, error) { t.Fatal("created"); return nil, nil }); err == nil {
		t.Fatal("legacy identity accepted")
	}
}

// Retirement cancels and settles every other request of the box, exempts only
// the exact self-target request, and leaves the socket pathname in place.
func TestNativeListenerRetireSettlesOthersAndExemptsSelf(t *testing.T) {
	l, name := nativeListenerFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	conns := make(chan net.Conn, 2)
	releaseSelf := make(chan struct{})
	otherCancelled := make(chan struct{})
	if err := l.Start(ctx, func(h context.Context, c net.Conn) {
		conns <- c
		var b [1]byte
		_, _ = c.Read(b[:])
		if b[0] == 's' {
			<-releaseSelf // the removal request finishes on its own schedule
			return
		}
		<-h.Done() // another request: only retirement cancellation ends it
		close(otherCancelled)
	}); err != nil {
		t.Fatal(err)
	}
	self := dialOperatorService(t, name)
	if _, err := self.Write([]byte("s")); err != nil {
		t.Fatal(err)
	}
	selfConn := <-conns
	other := dialOperatorService(t, name)
	if _, err := other.Write([]byte("o")); err != nil {
		t.Fatal(err)
	}
	<-conns
	r, err := l.Retire(ctx, selfConn)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-otherCancelled:
	default:
		t.Fatal("retirement returned before the other request settled")
	}
	serviceClosedPeer(t, other)
	if !r.SelfPending() {
		t.Fatal("self-target request was cancelled or awaited")
	}
	if _, err := os.Lstat(name); err != nil {
		t.Fatal("listener Close unlinked the socket:", err)
	}
	if _, err := net.Dial("unix", name); err == nil {
		t.Fatal("retired listener still accepts")
	}
	close(releaseSelf)
	if err := r.WaitSelf(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Retire(ctx, nil); err == nil {
		t.Fatal("second retirement accepted")
	}
}

func TestNativeListenerRefusesForeignSelfTarget(t *testing.T) {
	l, _ := nativeListenerFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := l.Start(ctx, func(context.Context, net.Conn) {}); err != nil {
		t.Fatal(err)
	}
	a, b := net.Pipe()
	defer func() { _ = a.Close(); _ = b.Close() }()
	if _, err := l.Retire(ctx, a); err == nil || !strings.Contains(err.Error(), "does not belong") {
		t.Fatal("foreign connection exempted:", err)
	}
	// The refusal happened before any state change: retirement still works.
	if _, err := l.Retire(ctx, nil); err != nil {
		t.Fatal(err)
	}
}

// A request that ignores cancellation keeps retirement unsettled; the bounded
// caller gets an error, never a settlement.
func TestNativeListenerRetireWaitsForUnsettledRequest(t *testing.T) {
	l, name := nativeListenerFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	release := make(chan struct{})
	accepted := make(chan struct{})
	if err := l.Start(ctx, func(context.Context, net.Conn) { close(accepted); <-release }); err != nil {
		t.Fatal(err)
	}
	_ = dialOperatorService(t, name)
	<-accepted
	bounded, stop := context.WithTimeout(ctx, 200*time.Millisecond)
	defer stop()
	if r, err := l.Retire(bounded, nil); err == nil || r != nil {
		t.Fatal("unsettled request produced a settlement")
	}
	close(release)
	if _, err := l.Retire(ctx, nil); err == nil {
		t.Fatal("failed retirement was retried")
	}
}
