package firecracker

import (
	"bufio"
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testVSockPath(t *testing.T) string {
	t.Helper()
	// Darwin's TMPDIR plus a test name can exceed sockaddr_un's path limit.
	dir, err := os.MkdirTemp("/tmp", "yfc-vsock-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	return filepath.Join(dir, "vsock")
}

func TestVSockHandshakePreservesGreetingAndHalfClose(t *testing.T) {
	path := testVSockPath(t)
	listener, e := net.Listen("unix", path)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = listener.Close() }()
	done := make(chan error, 1)
	go func() {
		c, e := listener.Accept()
		if e != nil {
			done <- e
			return
		}
		defer func() { _ = c.Close() }()
		line, e := bufio.NewReader(c).ReadString('\n')
		if e != nil || line != "CONNECT 5253\n" {
			done <- io.ErrUnexpectedEOF
			return
		}
		if _, e = c.Write([]byte("OK 42\nSSH-2.0-test\r\n")); e != nil {
			done <- e
			return
		}
		_, e = io.ReadAll(c)
		if e == nil {
			_, e = c.Write([]byte("final"))
		}
		done <- e
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	c, e := DialGuest(ctx, path, 5253)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = c.Close() }()
	buf := make([]byte, len("SSH-2.0-test\r\n"))
	if _, e = io.ReadFull(c, buf); e != nil || string(buf) != "SSH-2.0-test\r\n" {
		t.Fatalf("greeting: %q %v", buf, e)
	}
	if e = c.(interface{ CloseWrite() error }).CloseWrite(); e != nil {
		t.Fatal(e)
	}
	b, e := io.ReadAll(c)
	if e != nil || string(b) != "final" {
		t.Fatalf("half-close lost output: %q %v", b, e)
	}
	if e = <-done; e != nil {
		t.Fatal(e)
	}
}

func TestVSockHandshakeCancellation(t *testing.T) {
	path := testVSockPath(t)
	listener, e := net.Listen("unix", path)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = listener.Close() }()
	go func() {
		c, e := listener.Accept()
		if e == nil {
			defer func() { _ = c.Close() }()
			_, _ = io.Copy(io.Discard, c)
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	timer := time.AfterFunc(20*time.Millisecond, cancel)
	defer timer.Stop()
	started := time.Now()
	if _, e = DialGuest(ctx, path, 5253); e == nil {
		t.Fatal("accepted stalled handshake")
	}
	if time.Since(started) > time.Second {
		t.Fatal("handshake ignored cancellation")
	}
}
