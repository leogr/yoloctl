package openshell

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestRuntimeSSHPublicPathDirectoryPublishesAndCloses(t *testing.T) {
	for _, mode := range []os.FileMode{0700, 0755} {
		t.Run(fmt.Sprintf("%o", mode), func(t *testing.T) {
			name := t.TempDir()
			if err := os.Chmod(name, mode); err != nil {
				t.Fatal(err)
			}
			fd, err := unix.Open(name, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if err != nil {
				t.Fatal(err)
			}
			dir := os.NewFile(uintptr(fd), "original public relay directory")
			defer func() { _ = dir.Close() }()
			if mode == 0755 && runtimeDirectoryOwner(dir, os.Geteuid(), os.Getegid()) == nil {
				t.Fatal("private authority directory check was relaxed")
			}
			endpoint, err := listenRuntimeSSHAt(dir, os.Geteuid(), os.Getegid())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = endpoint.Close() }()
			info, err := os.Lstat(filepath.Join(name, "ssh.sock"))
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatal("public socket permissions", err)
			}
			// Renaming proves cleanup/fsync retain the original descriptor, not
			// a replacement subsequently published at its former pathname.
			moved := name + "-moved"
			if err := os.Rename(name, moved); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = endpoint.Close(); _ = os.Remove(moved) }()
			if err := os.Mkdir(name, 0700); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(name, "ssh.sock")
			if err := os.WriteFile(marker, []byte("replacement"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := endpoint.Close(); err != nil {
				t.Fatal(err)
			}
			if err := endpoint.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(filepath.Join(moved, "ssh.sock")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("original socket remained", err)
			}
			if b, err := os.ReadFile(marker); err != nil || string(b) != "replacement" {
				t.Fatal("replacement directory changed", err)
			}
		})
	}
}

func TestRuntimeSSHPublicDirectoryRejectsWritersAndWrongOwner(t *testing.T) {
	dir := runtimeSocketFixture(t)
	for _, mode := range []os.FileMode{0777, 0770, 0702, 0755} {
		if err := dir.Chmod(mode); err != nil {
			t.Fatal(err)
		}
		uid := os.Geteuid()
		if mode == 0755 {
			uid++
		}
		if endpoint, err := listenRuntimeSSHAt(dir, uid, os.Getegid()); err == nil || endpoint != nil {
			t.Fatal("untrusted public directory accepted")
		}
		if _, err := os.Lstat(filepath.Join(dir.Name(), "ssh.sock")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("refusal published an endpoint", err)
		}
	}
}

func runtimeSocketFixture(t *testing.T) *os.File {
	t.Helper()
	dir, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := dir.Chmod(0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dir.Close() })
	return dir
}

func TestRuntimeSSHRelayPreservesParallelStreamsAndHalfClose(t *testing.T) {
	dir := runtimeSocketFixture(t)
	target := fmt.Sprintf("/proc/self/fd/%d/target.sock", dir.Fd())
	upstream, err := net.ListenUnix("unix", &net.UnixAddr{Name: target, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = upstream.Close() }()
	if err := upstream.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	upstreamResults := make(chan error, 2)
	go func() {
		for range 2 {
			conn, err := upstream.AcceptUnix()
			if err != nil {
				upstreamResults <- err
				continue
			}
			go func() {
				defer func() { _ = conn.Close() }()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				raw, err := io.ReadAll(conn)
				if err == nil {
					_, err = conn.Write(append([]byte("response:"), raw...))
				}
				if err == nil {
					err = conn.CloseWrite()
				}
				upstreamResults <- err
			}()
		}
	}()
	endpoint, err := listenRuntimeSSHAt(dir, os.Geteuid(), os.Getegid())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = endpoint.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	served := make(chan error, 1)
	go func() {
		served <- endpoint.serveRuntimeSSH(ctx, target, func() bool { return true }, func() error { return nil })
	}()
	var clients sync.WaitGroup
	results := make(chan error, 2)
	for _, request := range []string{"one", "two"} {
		clients.Add(1)
		go func() {
			defer clients.Done()
			conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: fmt.Sprintf("/proc/self/fd/%d/ssh.sock", dir.Fd()), Net: "unix"})
			if err != nil {
				results <- err
				return
			}
			defer func() { _ = conn.Close() }()
			_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
			_, err = conn.Write([]byte(request))
			if err == nil {
				err = conn.CloseWrite()
			}
			if err != nil {
				results <- err
				return
			}
			raw, err := io.ReadAll(conn)
			if err == nil && string(raw) != "response:"+request {
				err = fmt.Errorf("half-close response changed: %q", raw)
			}
			results <- err
		}()
	}
	clients.Wait()
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
		if err := <-upstreamResults; err != nil {
			t.Fatal(err)
		}
	}
	cancel()
	select {
	case err := <-served:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runtime SSH relay did not stop")
	}
	if _, err := os.Lstat(filepath.Join(dir.Name(), "ssh.sock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("owned public socket leaked", err)
	}
}

func TestRuntimeSSHRefusesStalePublicationAndForeignCleanup(t *testing.T) {
	dir := runtimeSocketFixture(t)
	endpoint, err := listenRuntimeSSHAt(dir, os.Geteuid(), os.Getegid())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = endpoint.Close() }()
	if other, err := listenRuntimeSSHAt(dir, os.Geteuid(), os.Getegid()); err == nil {
		_ = other.Close()
		t.Fatal("existing socket was adopted")
	}
	name := filepath.Join(dir.Name(), "ssh.sock")
	if err := os.Remove(name); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte("foreign replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := endpoint.Close(); err == nil {
		t.Fatal("foreign replacement cleanup accepted")
	}
	if raw, err := os.ReadFile(name); err != nil || string(raw) != "foreign replacement" {
		t.Fatal("foreign socket replacement was removed")
	}
}

func TestRuntimeSSHDegradedAdmissionOpensNoPrivateConnection(t *testing.T) {
	dir := runtimeSocketFixture(t)
	endpoint, err := listenRuntimeSSHAt(dir, os.Geteuid(), os.Getegid())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = endpoint.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	served := make(chan error, 1)
	go func() {
		served <- endpoint.serveRuntimeSSH(ctx, "/unused-private-socket", func() bool { return false }, func() error { t.Error("degraded relay touched its private target"); return nil })
	}()
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: fmt.Sprintf("/proc/self/fd/%d/ssh.sock", dir.Fd()), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if raw, err := io.ReadAll(conn); err != nil || len(raw) != 0 {
		t.Fatal("degraded relay admitted a stream", err)
	}
	cancel()
	select {
	case err := <-served:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("degraded relay did not stop")
	}
}
