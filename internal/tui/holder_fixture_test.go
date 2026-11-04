package tui

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/session"
	"github.com/leogr/yoloctl/internal/termio"
	"github.com/leogr/yoloctl/internal/tui/term"
	"golang.org/x/crypto/ssh"
)

// A real session holder (session.RunHolder) behind a fake OpenShell SSH
// sandbox, and real managed views of it: each view is a TrustedView
// term.Session whose child relays the holder's framed session over the
// private view channel with session.Relay, as `box attach` does. Only the
// sandbox is fake; the holder, the protocol and the views are the
// production code.

type fakeSandbox struct {
	mu      sync.Mutex
	ch      ssh.Channel
	resizes [][2]int // cols, rows, in order
	started chan struct{}
}

func startFakeSandbox(t *testing.T, sock string) *fakeSandbox {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	fs := &fakeSandbox{started: make(chan struct{})}
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		_, chans, reqs, err := ssh.NewServerConn(conn, cfg)
		if err != nil {
			return
		}
		go func() {
			for r := range reqs {
				if r.WantReply {
					_ = r.Reply(true, nil)
				}
			}
		}()
		for newCh := range chans {
			if newCh.ChannelType() != "session" {
				_ = newCh.Reject(ssh.UnknownChannelType, "session only")
				continue
			}
			ch, chReqs, err := newCh.Accept()
			if err != nil {
				return
			}
			fs.mu.Lock()
			fs.ch = ch
			fs.mu.Unlock()
			go fs.requests(chReqs)
			go func() { // the agent's stdin is not scripted here
				buf := make([]byte, 4096)
				for {
					if _, err := ch.Read(buf); err != nil {
						return
					}
				}
			}()
		}
	}()
	return fs
}

func (fs *fakeSandbox) requests(reqs <-chan *ssh.Request) {
	for req := range reqs {
		switch req.Type {
		case "pty-req":
			_ = req.Reply(true, nil)
		case "exec":
			_ = req.Reply(true, nil)
			close(fs.started)
		case "window-change":
			var p struct{ Cols, Rows, W, H uint32 }
			_ = ssh.Unmarshal(req.Payload, &p)
			fs.mu.Lock()
			fs.resizes = append(fs.resizes, [2]int{int(p.Cols), int(p.Rows)})
			fs.mu.Unlock()
		default:
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
		}
	}
}

// emit writes agent output toward the holder.
func (fs *fakeSandbox) emit(t *testing.T, s string) {
	t.Helper()
	fs.mu.Lock()
	ch := fs.ch
	fs.mu.Unlock()
	if _, err := ch.Write([]byte(s)); err != nil {
		t.Fatal(err)
	}
}

// lastSize is the agent PTY size the holder last asked for.
func (fs *fakeSandbox) lastSize() [2]int {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if len(fs.resizes) == 0 {
		return [2]int{}
	}
	return fs.resizes[len(fs.resizes)-1]
}

// startManagedHolder runs a real holder for one fake agent and returns its
// attach socket and sandbox.
func startManagedHolder(t *testing.T) (string, *fakeSandbox) {
	t.Helper()
	// Short socket paths (macOS's 104-byte limit), as the session tests do.
	dir, err := os.MkdirTemp("", "yp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	opts := session.HolderOptions{
		Agentbox: "coder", SSHSocket: filepath.Join(dir, "s.sock"), AttachSocket: filepath.Join(dir, "a.sock"),
		MarkerPath: filepath.Join(dir, "active"), TombstonePath: filepath.Join(dir, "last-exit"),
		Argv: []string{"agent"}, Cols: 80, Rows: 24, Term: "xterm-256color", RingBytes: 1 << 16, TitleTemplate: "{title}",
	}
	fs := startFakeSandbox(t, opts.SSHSocket)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _, _ = session.RunHolder(ctx, opts) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("holder did not stop")
		}
	})
	select {
	case <-fs.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the holder never started its agent")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(opts.AttachSocket); err == nil {
			return opts.AttachSocket, fs
		}
		if time.Now().After(deadline) {
			t.Fatal("the holder never served its attach socket")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestManagedRelayChild is the managed view's child process: it relays the
// holder's framed session to the frontend's private channel, as `box
// attach` does after admission.
func TestManagedRelayChild(t *testing.T) {
	sock := os.Getenv("YOLOCTL_TEST_RELAY_SOCK")
	if sock == "" {
		return
	}
	local, err := termio.InheritedView()
	if err != nil || local == nil {
		os.Exit(80)
	}
	remote, err := net.Dial("unix", sock)
	if err != nil {
		os.Exit(81)
	}
	if err := session.Relay(context.Background(), remote, local, os.Stdin); err != nil {
		os.Exit(82)
	}
	os.Exit(0)
}

// managedView starts a real managed view of the holder at sock, sized
// rows×cols, for a pane or tab.
func managedView(t *testing.T, sock string, rows, cols int) *term.Session {
	t.Helper()
	s, err := term.Start(term.Options{
		Argv:        []string{os.Args[0], "-test.run=^TestManagedRelayChild$"},
		Env:         append(os.Environ(), "YOLOCTL_TEST_RELAY_SOCK="+sock),
		TrustedView: true, Rows: rows, Cols: cols, CellWidth: 8, CellHeight: 16,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}
