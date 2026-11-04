package vmwire

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

func TestTCPStreamHalfClose(t *testing.T) {
	a, b := net.Pipe() // Carrier has no half-close support.
	defer func() { _ = a.Close() }()
	defer func() { _ = b.Close() }()
	_ = a.SetDeadline(time.Now().Add(3 * time.Second))
	_ = b.SetDeadline(time.Now().Add(3 * time.Second))
	client, server := NewTCPStream(a, a), NewTCPStream(b, b)
	payload := bytes.Repeat([]byte("request"), maxTCPFrame)
	done := make(chan error, 1)
	go func() {
		data, err := io.ReadAll(server)
		if err == nil && !bytes.Equal(data, payload) {
			err = errors.New("request corrupted")
		}
		if err == nil {
			_, err = server.Write([]byte("reply after EOF"))
		}
		if err == nil {
			err = server.CloseWrite()
		}
		done <- err
	}()
	if _, err := client.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := client.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if err := client.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Write([]byte("late")); !errors.Is(err, net.ErrClosed) {
		t.Fatal(err)
	}
	reply, err := io.ReadAll(client)
	if err != nil || string(reply) != "reply after EOF" {
		t.Fatalf("reply %q: %v", reply, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestTCPStreamRejectsBrokenFrames(t *testing.T) {
	header := make([]byte, 4)
	binary.BigEndian.PutUint32(header, maxTCPFrame+1)
	for _, data := range [][]byte{nil, {0, 0}, {0, 0, 0, 2, 'x'}, header} {
		a, b := net.Pipe()
		go func() { _, _ = b.Write(data); _ = b.Close() }()
		stream := NewTCPStream(a, a)
		_, err := io.ReadAll(stream)
		_ = a.Close()
		if err == nil {
			t.Errorf("accepted truncated/oversize frame %v", data)
		}
		if _, next := stream.Read(make([]byte, 1)); next != err {
			t.Errorf("broken framing became readable: %v -> %v", err, next)
		}
	}
}

func tcpPair(t *testing.T) (*net.TCPConn, *net.TCPConn) {
	t.Helper()
	ln, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	a, err := net.DialTCP("tcp", nil, ln.Addr().(*net.TCPAddr))
	if err != nil {
		t.Fatal(err)
	}
	b, err := ln.AcceptTCP()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []*net.TCPConn{a, b} {
		_ = c.SetDeadline(time.Now().Add(3 * time.Second))
		t.Cleanup(func() { _ = c.Close() })
	}
	return a, b
}

func TestTCPBridgeHalfCloseEitherDirection(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(map[bool]string{false: "client EOF", true: "server EOF"}[reverse], func(t *testing.T) {
			client, host := tcpPair(t)
			guest, server := tcpPair(t)
			a, b := net.Pipe()
			hostDone, guestDone := make(chan struct{}), make(chan struct{})
			go func() { BridgeTCP(host, NewTCPStream(a, a)); close(hostDone) }()
			go func() { BridgeTCP(NewTCPStream(b, b), guest); close(guestDone) }()
			if reverse {
				client, server = server, client
			}
			_ = client.CloseWrite()
			empty, err := io.ReadAll(server)
			if err != nil || len(empty) != 0 {
				t.Fatalf("EOF: %q %v", empty, err)
			}
			_, err = server.Write([]byte("late reply"))
			if err != nil {
				t.Fatal(err)
			}
			_ = server.CloseWrite()
			reply, err := io.ReadAll(client)
			if err != nil || string(reply) != "late reply" {
				t.Fatalf("reply %q %v", reply, err)
			}
			for _, done := range []chan struct{}{hostDone, guestDone} {
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Fatal("bridge stuck")
				}
			}
		})
	}
}

func TestTCPBridgeCancellation(t *testing.T) {
	client, host := tcpPair(t)
	a, b := net.Pipe()
	defer func() { _ = b.Close() }()
	done := make(chan struct{})
	go func() { BridgeTCP(host, NewTCPStream(a, a)); close(done) }()
	_ = a.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("canceled bridge stuck")
	}
	_, err := client.Read(make([]byte, 1))
	if err == nil {
		t.Fatal("client not closed")
	}
}
