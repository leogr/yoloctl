package sshtransport

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"os"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func testServer(t *testing.T, handle func(ssh.Channel, <-chan *ssh.Request)) (net.Conn, *ssh.ClientConfig) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		server, channels, requests, err := ssh.NewServerConn(conn, config)
		if err != nil {
			return
		}
		defer func() { _ = server.Close() }()
		go ssh.DiscardRequests(requests)
		for channel := range channels {
			if channel.ChannelType() != "session" {
				_ = channel.Reject(ssh.UnknownChannelType, "session only")
				continue
			}
			c, reqs, err := channel.Accept()
			if err != nil {
				return
			}
			handle(c, reqs)
		}
	}()
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return conn, &ssh.ClientConfig{User: "test", HostKeyCallback: ssh.FixedHostKey(signer.PublicKey())}
}

func TestRunPreservesStreamsEOFAndExitStatus(t *testing.T) {
	payload := bytes.Repeat([]byte{0, 1, 2, 10, 13, 127, 255}, 50000)
	conn, config := testServer(t, func(c ssh.Channel, requests <-chan *ssh.Request) {
		defer func() { _ = c.Close() }()
		for req := range requests {
			if req.Type != "exec" {
				_ = req.Reply(false, nil)
				continue
			}
			_ = req.Reply(true, nil)
			_, _ = io.Copy(c, c)
			_, _ = c.Stderr().Write([]byte("separate stderr\n"))
			_, _ = c.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{42}))
			return
		}
	})
	in, out, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = in.Close() }()
	go func() { _, _ = out.Write(payload); _ = out.Close() }()
	var stdout, stderr bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = Run(ctx, conn, config, "binary echo", false, in, &stdout, &stderr)
	if code, ok := ExitStatus(err); !ok || code != 42 {
		t.Fatalf("exit: %v", err)
	}
	if !bytes.Equal(stdout.Bytes(), payload) || stderr.String() != "separate stderr\n" {
		t.Fatal("stream bytes changed")
	}
}

func TestCommandCanFinishWithOpenInput(t *testing.T) {
	conn, config := testServer(t, func(c ssh.Channel, requests <-chan *ssh.Request) {
		defer func() { _ = c.Close() }()
		for req := range requests {
			_ = req.Reply(true, nil)
			if req.Type == "exec" {
				_, _ = c.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
				return
			}
		}
	})
	in, out, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = in.Close(); _ = out.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = Run(ctx, conn, config, "true", false, in, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
}

func TestCancellationBoundsHandshake(t *testing.T) {
	conn, peer := net.Pipe()
	defer func() { _ = peer.Close() }()
	in, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = in.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = Run(ctx, conn, &ssh.ClientConfig{User: "test", HostKeyCallback: ssh.InsecureIgnoreHostKey()}, "true", false, in, io.Discard, io.Discard)
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("unbounded handshake: %v", err)
	}
}

func TestConnectCarriesSequentialSessions(t *testing.T) {
	conn, config := testServer(t, func(c ssh.Channel, requests <-chan *ssh.Request) {
		defer func() { _ = c.Close() }()
		for req := range requests {
			var exec struct{ Command string }
			if req.Type != "exec" || ssh.Unmarshal(req.Payload, &exec) != nil {
				_ = req.Reply(false, nil)
				continue
			}
			_ = req.Reply(true, nil)
			_, _ = io.WriteString(c, exec.Command)
			_, _ = c.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
			return
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := Connect(ctx, conn, config)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	for _, command := range []string{"first", "second"} {
		s, err := client.NewSession()
		if err != nil {
			t.Fatal(err)
		}
		out, err := s.Output(command)
		_ = s.Close()
		if err != nil || string(out) != command {
			t.Fatalf("%s: %q %v", command, out, err)
		}
	}
}

func TestConnectBoundsHandshakeAndClosesConn(t *testing.T) {
	conn, peer := net.Pipe()
	defer func() { _ = peer.Close() }()
	go func() { _, _ = io.Copy(io.Discard, peer) }()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	client, err := Connect(ctx, conn, &ssh.ClientConfig{User: "test", HostKeyCallback: ssh.InsecureIgnoreHostKey()})
	if err == nil || client != nil || time.Since(start) > time.Second {
		t.Fatalf("unbounded handshake: %v", err)
	}
	if _, err = conn.Write([]byte{0}); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("failed handshake left its connection open: %v", err)
	}
}

func TestHostIdentityIsVerified(t *testing.T) {
	conn, config := testServer(t, func(c ssh.Channel, _ <-chan *ssh.Request) { _ = c.Close() })
	want := errors.New("wrong VM identity")
	config.HostKeyCallback = func(string, net.Addr, ssh.PublicKey) error { return want }
	in, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = in.Close() }()
	if err = Run(context.Background(), conn, config, "true", false, in, io.Discard, io.Discard); !errors.Is(err, want) {
		t.Fatalf("identity rejection lost: %v", err)
	}
}
