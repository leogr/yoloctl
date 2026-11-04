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
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if ready, err := probeRuntimeReadyAt(ctx, dir, signer.PublicKey(), os.Geteuid(), os.Getegid()); ready || err != nil {
		t.Fatal("stopped SSH backlog counted as readiness", ready, err)
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
