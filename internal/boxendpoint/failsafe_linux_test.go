//go:build linux

package boxendpoint

import (
	"bufio"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// waitNoConns waits until srv has let go of every connection.
func waitNoConns(t *testing.T, srv *Server) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		srv.mu.Lock()
		n := len(srv.conns)
		srv.mu.Unlock()
		if n == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("endpoint kept a connection")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// stalledPeer connects, reads the endpoint's greeting and sends nothing.
func stalledPeer(t *testing.T, sock string) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	reader := bufio.NewReader(conn)
	if line, err := reader.ReadString('\n'); err != nil || !strings.HasPrefix(line, "SSH-2.0-yoloctl_box_endpoint_") {
		t.Fatalf("greeting = %q, %v", line, err)
	}
	return conn, reader
}

// A peer that never completes its SSH handshake keeps its connection until
// the fail-safe, then loses it.
func TestStalledHandshakeEndsAtFailSafe(t *testing.T) {
	if handshakeFailSafe != 2*time.Minute {
		t.Fatal("handshake fail-safe changed", handshakeFailSafe)
	}
	sock, srv := startEndpoint(t, nil)
	conn, reader := stalledPeer(t, sock)
	_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	var timeout net.Error
	if _, err := reader.ReadByte(); !errors.As(err, &timeout) || !timeout.Timeout() {
		t.Fatal("stalled handshake dropped before the fail-safe", err)
	}
	_ = conn.Close()
	waitNoConns(t, srv)

	old := handshakeFailSafe
	handshakeFailSafe = 300 * time.Millisecond
	defer func() { handshakeFailSafe = old }()
	conn, reader = stalledPeer(t, sock)
	_ = conn.SetReadDeadline(time.Now().Add(20 * time.Second))
	if _, err := reader.ReadByte(); !errors.Is(err, io.EOF) {
		t.Fatal("stalled handshake outlived the fail-safe", err)
	}
	waitNoConns(t, srv)
}

func TestDefaultCloseTimeoutIsFailSafe(t *testing.T) {
	_, srv := startEndpoint(t, nil)
	if DefaultCloseTimeout != 2*time.Minute || srv.cfg.CloseTimeout != DefaultCloseTimeout {
		t.Fatal("close fail-safe changed", DefaultCloseTimeout, srv.cfg.CloseTimeout)
	}
}
