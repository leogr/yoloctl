package sandbox

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/container"
	"golang.org/x/crypto/ssh"
)

func TestStopSignalsOnlyThroughConfinedExecWithoutPTY(t *testing.T) {
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
	defer func() { _ = ln.Close() }()
	commands := make(chan string, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
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
		if server.User() != "sandbox" {
			t.Errorf("unexpected SSH user %q", server.User())
		}
		go ssh.DiscardRequests(requests)
		channel, ok := <-channels
		if !ok {
			return
		}
		if channel.ChannelType() != "session" {
			t.Errorf("unexpected channel %q", channel.ChannelType())
			return
		}
		c, reqs, err := channel.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		for req := range reqs {
			if req.Type != "exec" {
				t.Errorf("shutdown requested %q instead of confined exec", req.Type)
				_ = req.Reply(false, nil)
				continue
			}
			var payload struct{ Command string }
			if err := ssh.Unmarshal(req.Payload, &payload); err != nil {
				t.Error(err)
				return
			}
			commands <- payload.Command
			_ = req.Reply(true, nil)
			// The supervisor exits when sleep receives TERM; losing the SSH
			// connection without an exit status is expected in that case.
			return
		}
	}()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_ = signalWorkloads(ctx, conn)
	<-done
	select {
	case command := <-commands:
		if command != "exec /bin/sh -c 'kill -TERM -1'" {
			t.Fatalf("shutdown command escaped the sandbox scope: %q", command)
		}
	default:
		t.Fatal("no confined exec request")
	}
}

func TestStopSignalBoundsStalledSSH(t *testing.T) {
	conn, peer := net.Pipe()
	defer func() { _ = peer.Close() }()
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := signalWorkloads(ctx, conn); err == nil || time.Since(start) > time.Second {
		t.Fatalf("stalled endpoint did not respect deadline: %v", err)
	}
}

func TestStopFallsBackWhenSandboxEndpointIsUnavailable(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LIMA_HOME", t.TempDir())
	bin := t.TempDir()
	log := filepath.Join(bin, "calls")
	t.Setenv("STOP_TEST_CALLS", log)
	id := strings.Repeat("d", 64)
	t.Setenv("STOP_TEST_CID", id)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$STOP_TEST_CALLS"
case "$*" in
  *"socat STDIO UNIX-CONNECT:/run/yoloctl/probe/ssh.sock"*) exit 1 ;;
  *"nerdctl inspect probe"*) printf '[{"Id":"%s","Name":"probe"}]\n' "$STOP_TEST_CID" ;;
  *"nerdctl stop --time 30 $STOP_TEST_CID"*) exit 0 ;;
  *) exit 37 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "limactl"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	probes := 0
	fixtureTimeout := func(ctx context.Context, budget time.Duration) (context.Context, context.CancelFunc) {
		probes++
		if budget != 3*time.Second {
			t.Errorf("production probe budget changed: %v", budget)
		}
		return context.WithTimeout(ctx, 30*time.Second)
	}
	if err := stopWithProbeTimeout(t.Context(), container.New("trial"), "probe", fixtureTimeout); err != nil {
		t.Fatal(err)
	}
	if probes != 1 {
		t.Fatalf("expected one bounded endpoint probe, got %d", probes)
	}
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	calls := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(calls) != 3 || !strings.Contains(calls[0], "socat STDIO") || !strings.Contains(calls[1], "nerdctl inspect probe") || !strings.Contains(calls[2], "nerdctl stop --time 30 "+id) {
		t.Fatalf("lost signal/stop ordering and bounded fallback: %q", calls)
	}
	if err := os.WriteFile(log, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := StopContainerID(t.Context(), container.New("trial"), "probe", id); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	calls = strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(calls) != 2 || !strings.Contains(calls[0], "UNIX-CONNECT:/run/yoloctl/probe/ssh.sock") || !strings.Contains(calls[1], "nerdctl stop --time 30 "+id) {
		t.Fatal("known CID repeated lookup or replaced named SSH endpoint", calls)
	}
	if err := Stop(t.Context(), container.New("trial"), "../unsafe"); err == nil {
		t.Fatal("accepted invalid socket identity")
	}
}

func TestStopProbeDefaultsAndParentCancellation(t *testing.T) {
	before := time.Now()
	ctx, cancel := stopProbeContext(t.Context(), nil)
	after := time.Now()
	deadline, ok := ctx.Deadline()
	if !ok || deadline.Before(before.Add(3*time.Second)) || deadline.After(after.Add(3*time.Second)) {
		cancel()
		t.Fatal("default probe budget changed", deadline)
	}
	cancel()
	if ctx.Err() != context.Canceled {
		t.Fatal("probe cancel did not propagate", ctx.Err())
	}
	parentDeadline := time.Now().Add(time.Second)
	parent, cancelParent := context.WithDeadline(t.Context(), parentDeadline)
	defer cancelParent()
	ctx, cancel = stopProbeContext(parent, nil)
	defer cancel()
	if deadline, ok := ctx.Deadline(); !ok || !deadline.Equal(parentDeadline) {
		t.Fatal("earlier parent deadline lost", deadline)
	}
	cancelParent()
	if ctx.Err() != context.Canceled {
		t.Fatal("parent cancellation lost", ctx.Err())
	}
}
