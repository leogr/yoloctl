package openshell

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestRuntimeReadinessRequiresRespondingSSHAndTheGenerationKey(t *testing.T) {
	dir := runtimeSocketFixture(t)
	if err := os.Mkdir(filepath.Join(dir.Name(), "ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	private := func(name string) string { return fmt.Sprintf("/proc/self/fd/%d/%s", dir.Fd(), name) }
	health, err := net.ListenUnix("unix", &net.UnixAddr{Name: private("ready.sock"), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = health.Close() }()
	server, err := net.ListenUnix("unix", &net.UnixAddr{Name: private("ssh/ssh.sock"), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = server.Close() }()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			conn, err := health.AcceptUnix()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	go func() {
		for {
			conn, err := server.AcceptUnix()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
				config := &ssh.ServerConfig{NoClientAuth: true}
				config.AddHostKey(signer)
				transport, _, _, err := ssh.NewServerConn(conn, config)
				if err == nil {
					_ = transport.Wait()
					_ = transport.Close()
				}
			}()
		}
	}()
	ready, err := probeRuntimeReadyAt(context.Background(), dir, signer.PublicKey(), os.Geteuid(), os.Getegid())
	if err != nil || !ready {
		t.Fatal("matching SSH transport was not ready", ready, err)
	}
	_, other, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	otherSigner, err := ssh.NewSignerFromKey(other)
	if err != nil {
		t.Fatal(err)
	}
	if ready, err := probeRuntimeReadyAt(context.Background(), dir, otherSigner.PublicKey(), os.Geteuid(), os.Getegid()); ready || !errors.Is(err, errRuntimeSSHKey) {
		t.Fatal("wrong generation key accepted", ready, err)
	}
	_ = server.Close()
	// A bound Unix listener without an SSH responder can accept into its
	// backlog. It must still be unready rather than mistaken for healthy.
	stalled, err := net.ListenUnix("unix", &net.UnixAddr{Name: private("ssh/ssh.sock"), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stalled.Close() }()

	// Cancel as an explicit stop would, after the private SSH connection is
	// accepted and while it is still waiting for the server handshake.
	probeCtx, cancelProbe := context.WithCancel(t.Context())
	defer cancelProbe()
	probeReturned := make(chan struct{})
	go func() {
		conn, err := stalled.AcceptUnix()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		cancelProbe()
		<-probeReturned
	}()
	probeReady, probeErr := probeRuntimeReadyAt(probeCtx, dir, signer.PublicKey(), os.Geteuid(), os.Getegid())
	close(probeReturned)
	if probeReady || probeErr != nil || probeCtx.Err() == nil {
		t.Fatal("expected stop to interrupt the probe", probeReady, probeErr, probeCtx.Err())
	}
	if stopping, ordered := runtimeStopAfterProbe(probeCtx, "ready", probeErr); !stopping || !ordered {
		t.Fatal("stop during the probe lost previous readiness", stopping, ordered)
	}
	if stopping, ordered := runtimeStopAfterProbe(probeCtx, "degraded", probeErr); !stopping || ordered {
		t.Fatal("stop during the probe did not preserve degradation", stopping, ordered)
	}
	if stopping, ordered := runtimeStopAfterProbe(t.Context(), "ready", nil); stopping || ordered {
		t.Fatal("a completed probe was treated as cancelled", stopping, ordered)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if ready, err := probeRuntimeReadyAt(ctx, dir, signer.PublicKey(), os.Geteuid(), os.Getegid()); ready || err != nil {
		t.Fatal("stopped SSH backlog counted as readiness", ready, err)
	}

	// A silent connected peer and no caller deadline: the try runs out of its
	// own time. That is not a definite answer, so the monitor keeps its
	// previous readiness (and SSH admission) and tries again.
	start := time.Now()
	if ready, err := probeRuntimeReadyAt(t.Context(), dir, signer.PublicKey(), os.Geteuid(), os.Getegid()); ready || !errors.Is(err, errRuntimeProbeTimedOut) {
		t.Fatal("a silent peer was not reported as a timed-out try", ready, err)
	}
	if elapsed := time.Since(start); elapsed < time.Second || elapsed > 3*time.Second {
		t.Fatal("the try did not end at its own limit", elapsed)
	}
	// A definite answer (the endpoint is gone) is still plain "not ready".
	_ = stalled.Close()
	if err := os.Remove(filepath.Join(dir.Name(), "ssh", "ssh.sock")); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if ready, err := probeRuntimeReadyAt(t.Context(), dir, signer.PublicKey(), os.Geteuid(), os.Getegid()); ready || err != nil {
		t.Fatal("a missing endpoint was not a definite answer", ready, err)
	}
}

func TestRuntimePrivateSocketRefusesSymlinkAndRegularFile(t *testing.T) {
	dir := runtimeSocketFixture(t)
	if err := os.WriteFile(filepath.Join(dir.Name(), "file"), []byte("not a socket"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("file", filepath.Join(dir.Name(), "alias")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"file", "alias", "../outside"} {
		if err := checkRuntimePrivateSocket(dir, name, os.Geteuid(), os.Getegid()); err == nil {
			t.Fatal("untrusted private endpoint accepted", name)
		}
	}
}

// The connection's deadline can fire before the try's context reports its
// own: that I/O timeout is still a try that ran out of time, not a definite
// failure, so a ready box keeps SSH admission. An explicit stop (the parent
// ended) and a refused dial stay definite.
func TestProbeRanOutOfTimeIncludesTheSocketDeadline(t *testing.T) {
	live := t.Context()
	try, cancelTry := context.WithTimeout(live, time.Hour)
	defer cancelTry()
	socketTimeout := &net.OpError{Op: "read", Net: "unix", Err: os.ErrDeadlineExceeded}
	if !probeRanOutOfTime(live, try, socketTimeout) {
		t.Fatal("a socket deadline before the try's timer counted as definite")
	}
	expired, cancelExpired := context.WithTimeout(live, time.Nanosecond)
	defer cancelExpired()
	<-expired.Done()
	if !probeRanOutOfTime(live, expired, nil) {
		t.Fatal("the try's own deadline was not a timeout")
	}
	stopped, stop := context.WithCancel(live)
	stop()
	if probeRanOutOfTime(stopped, expired, socketTimeout) {
		t.Fatal("an explicit stop counted as a timeout")
	}
	if probeRanOutOfTime(live, try, syscall.ECONNREFUSED) {
		t.Fatal("a refused dial counted as a timeout")
	}
	// A definite refusal keeps precedence even when the try's own deadline
	// has already fired by the time it is classified.
	if probeRanOutOfTime(live, expired, &net.OpError{Op: "dial", Net: "unix", Err: syscall.ECONNREFUSED}) {
		t.Fatal("a refused dial after the try's deadline counted as a timeout")
	}
}
