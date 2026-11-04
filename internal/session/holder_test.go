package session

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/terminal"
	"golang.org/x/crypto/ssh"
	"gopkg.in/yaml.v3"
)

func readSessionFile[T any](path string) (T, error) {
	var value T
	data, err := os.ReadFile(path)
	if err != nil {
		return value, err
	}
	if err := yaml.Unmarshal(data, &value); err != nil {
		return value, err
	}
	return value, nil
}

// fakeSandbox is an in-process stand-in for openshell-sandbox's SSH
// endpoint: plain SSH over a unix socket, auth-none, one session channel
// whose "agent" the test scripts by writing output and sending an
// exit-status. It records what the holder asked for (pty, exec,
// window-change, keepalives).
type fakeSandbox struct {
	t  *testing.T
	ln net.Listener

	mu           sync.Mutex
	ptyTerm      string
	ptyCols      int
	ptyRows      int
	ptyPixels    [2]int
	execCmd      string
	resizes      [][2]int // cols, rows
	resizePixels [][2]int
	keepalives   int
	stdin        bytes.Buffer
	execs        []string // every exec, the agent's first
	sessions     int
	later        func() // optional, run when a later session's exec arrives

	started chan struct{} // closed when exec is accepted
	ch      ssh.Channel   // the session channel, for the test to script
}

func startFakeSandbox(t *testing.T, sockPath string) *fakeSandbox {
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

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	fs := &fakeSandbox{t: t, ln: ln, started: make(chan struct{})}
	t.Cleanup(func() { _ = ln.Close() })

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
				if r.Type == "keepalive@yoloctl.dev" {
					fs.mu.Lock()
					fs.keepalives++
					fs.mu.Unlock()
				}
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
			// The first session is the agent the test scripts; later
			// ones (the daemon reaper after the exit) succeed at once.
			fs.mu.Lock()
			n := fs.sessions
			fs.sessions++
			if n == 0 {
				fs.ch = ch
			}
			fs.mu.Unlock()
			go fs.handleRequests(ch, n, chReqs)
			go func() {
				buf := make([]byte, 4096)
				for {
					n, err := ch.Read(buf)
					if n > 0 {
						fs.mu.Lock()
						fs.stdin.Write(buf[:n])
						fs.mu.Unlock()
					}
					if err != nil {
						return
					}
				}
			}()
		}
	}()
	return fs
}

func (fs *fakeSandbox) handleRequests(ch ssh.Channel, n int, reqs <-chan *ssh.Request) {
	for req := range reqs {
		switch req.Type {
		case "pty-req":
			var p struct {
				Term          string
				Cols, Rows    uint32
				Width, Height uint32
				Modes         string
			}
			_ = ssh.Unmarshal(req.Payload, &p)
			fs.mu.Lock()
			fs.ptyTerm, fs.ptyCols, fs.ptyRows = p.Term, int(p.Cols), int(p.Rows)
			fs.ptyPixels = [2]int{int(p.Width), int(p.Height)}
			fs.mu.Unlock()
			_ = req.Reply(true, nil)
		case "exec":
			var p struct{ Command string }
			_ = ssh.Unmarshal(req.Payload, &p)
			fs.mu.Lock()
			fs.execs = append(fs.execs, p.Command)
			if n == 0 {
				fs.execCmd = p.Command
			}
			fs.mu.Unlock()
			_ = req.Reply(true, nil)
			if n > 0 {
				fs.mu.Lock()
				later := fs.later
				fs.mu.Unlock()
				if later != nil {
					later()
				}
				_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
				_ = ch.Close()
				continue
			}
			close(fs.started)
		case "window-change":
			var p struct{ Cols, Rows, W, H uint32 }
			_ = ssh.Unmarshal(req.Payload, &p)
			fs.mu.Lock()
			fs.resizes = append(fs.resizes, [2]int{int(p.Cols), int(p.Rows)})
			fs.resizePixels = append(fs.resizePixels, [2]int{int(p.W), int(p.H)})
			fs.mu.Unlock()
		default:
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
		}
	}
}

// emit writes agent output toward the holder.
func (fs *fakeSandbox) emit(s string) {
	fs.mu.Lock()
	ch := fs.ch
	fs.mu.Unlock()
	if _, err := ch.Write([]byte(s)); err != nil {
		fs.t.Logf("fake sandbox write: %v", err)
	}
}

// exit ends the scripted agent with status, mimicking sshd: an
// exit-status request followed by channel close.
func (fs *fakeSandbox) exit(status int) {
	fs.mu.Lock()
	ch := fs.ch
	fs.mu.Unlock()
	payload := ssh.Marshal(struct{ Status uint32 }{uint32(status)})
	if _, err := ch.SendRequest("exit-status", false, payload); err != nil {
		fs.t.Logf("fake sandbox exit-status: %v", err)
	}
	_ = ch.Close()
}

// closeStatusless drops the channel without an exit-status, like a
// crashed sandbox.
func (fs *fakeSandbox) closeStatusless() {
	fs.mu.Lock()
	ch := fs.ch
	fs.mu.Unlock()
	_ = ch.Close()
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// testPaths returns holder option paths inside a short tmp dir (unix
// socket paths have a ~104-byte limit).
func testPaths(t *testing.T) (opts HolderOptions, dir string) {
	t.Helper()
	// NOT t.TempDir(): it embeds the (long) test name in the path, and on
	// macOS a Unix socket path over ~104 bytes fails bind() with EINVAL
	// (sun_path limit). A short MkdirTemp prefix keeps s.sock/a.sock within
	// the limit regardless of test-function name length.
	d, err := os.MkdirTemp("", "ys")
	if err != nil {
		t.Fatalf("mkdir temp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	dir = d
	return HolderOptions{
		Agentbox:      "box",
		SSHSocket:     filepath.Join(dir, "s.sock"),
		AttachSocket:  filepath.Join(dir, "a.sock"),
		MarkerPath:    filepath.Join(dir, "active"),
		TombstonePath: filepath.Join(dir, "last-exit"),
		Argv:          []string{"agent", "--flag", "it's"},
		Cols:          100,
		Rows:          30,
		Term:          "xterm-256color",
		RingBytes:     1 << 16,
		// Output-focused tests assert exact bytes. Title composition has its
		// own lifecycle tests that override this explicit opt-out.
		TitleTemplate: "{title}",
		Logf:          t.Logf,
	}, dir
}

type holderResult struct {
	status int
	err    error
}

func runHolderAsync(ctx context.Context, opts HolderOptions) chan holderResult {
	res := make(chan holderResult, 1)
	go func() {
		status, err := RunHolder(ctx, opts)
		res <- holderResult{status, err}
	}()
	return res
}

// attachClient wraps session.Attach for tests: collects output, exposes
// stdin and resize, reports the final error.
type attachClient struct {
	mu     sync.Mutex
	out    bytes.Buffer
	stdinW io.WriteCloser
	resize chan terminal.Geometry
	done   chan error
}

func (a *attachClient) output() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.out.String()
}

func (a *attachClient) Write(p []byte) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.out.Write(p)
}

// wait returns the attach result. It fails the test rather than blocking
// until the package timeout (which also hides every later test's result)
// when the holder never releases the client.
func (a *attachClient) wait(t *testing.T) error {
	t.Helper()
	select {
	case err := <-a.done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the attach client to return")
		return nil
	}
}

func attach(t *testing.T, ctx context.Context, sock string) *attachClient {
	t.Helper()
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial attach socket: %v", err)
	}
	return attachConn(ctx, conn)
}

func attachConn(ctx context.Context, conn net.Conn) *attachClient {
	pr, pw := io.Pipe()
	a := &attachClient{stdinW: pw, resize: make(chan terminal.Geometry, 1), done: make(chan error, 1)}
	go func() {
		a.done <- Attach(ctx, conn, AttachIO{Stdin: pr, Stdout: a, Resize: a.resize})
	}()
	return a
}

func TestHolderLifecycle(t *testing.T) {
	opts, _ := testPaths(t)
	opts.KeepaliveInterval = 50 * time.Millisecond
	opts.LaunchToken = strings.Repeat("ab", 16)
	fs := startFakeSandbox(t, opts.SSHSocket)

	ctx := context.Background()
	res := runHolderAsync(ctx, opts)
	<-fs.started

	// The holder asked for the PTY and exec we configured.
	fs.mu.Lock()
	term, cols, rows, execCmd := fs.ptyTerm, fs.ptyCols, fs.ptyRows, fs.execCmd
	fs.mu.Unlock()
	if term != "xterm-256color" || cols != 100 || rows != 30 {
		t.Fatalf("pty = %s %dx%d", term, cols, rows)
	}
	if want := `'agent' '--flag' 'it'\''s'`; execCmd != want {
		t.Fatalf("exec = %s, want %s", execCmd, want)
	}
	if m, err := readSessionFile[Marker](opts.MarkerPath); err != nil || m.LaunchToken != opts.LaunchToken {
		t.Fatalf("marker = %+v, %v; want launch token %s", m, err, opts.LaunchToken)
	}

	// Output emitted while detached reaches the first client. Depending on
	// SSH/test-scheduler timing it may be replayed immediately or coalesce
	// with the next live write; the second attach below proves replay.
	fs.emit("early ")
	a := attachWhenReady(t, opts.AttachSocket)

	// Live output reaches it too.
	fs.emit("live")
	waitFor(t, "live data to client A", func() bool { return a.output() == "early live" })

	// A second client mirrors: replay covers everything so far.
	b := attach(t, ctx, opts.AttachSocket)
	waitFor(t, "replay to client B", func() bool { return b.output() == "early live" })

	// Client input reaches the agent's stdin.
	if _, err := a.stdinW.Write([]byte("typed!")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "stdin at sandbox", func() bool {
		fs.mu.Lock()
		defer fs.mu.Unlock()
		return fs.stdin.String() == "typed!"
	})
	if _, err := b.stdinW.Write([]byte("and B!")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "both clients' input at sandbox", func() bool {
		fs.mu.Lock()
		defer fs.mu.Unlock()
		return fs.stdin.String() == "typed!and B!"
	})

	// Any client's resize applies.
	b.resize <- terminal.Geometry{Cols: 121, Rows: 33}
	waitFor(t, "window-change at sandbox", func() bool {
		fs.mu.Lock()
		defer fs.mu.Unlock()
		return len(fs.resizes) == 1 && fs.resizes[0] == [2]int{121, 33}
	})
	a.resize <- terminal.Geometry{Cols: 100, Rows: 30}
	waitFor(t, "first client can resize again", func() bool {
		fs.mu.Lock()
		defer fs.mu.Unlock()
		return len(fs.resizes) == 2 && fs.resizes[1] == [2]int{100, 30}
	})

	// Keepalives tick (spike F3).
	waitFor(t, "keepalives", func() bool {
		fs.mu.Lock()
		defer fs.mu.Unlock()
		return fs.keepalives >= 2
	})

	// Agent exit: both clients get EXIT, holder returns the status,
	// tombstone written, marker removed, socket cleaned up.
	fs.emit("bye")
	fs.exit(7)

	r := <-res
	if r.err != nil || r.status != 7 {
		t.Fatalf("RunHolder = %d, %v; want 7, nil", r.status, r.err)
	}
	for name, c := range map[string]*attachClient{"A": a, "B": b} {
		err := <-c.done
		var ee *ExitError
		if !errors.As(err, &ee) || ee.Status != 7 {
			t.Fatalf("client %s attach result = %v, want ExitError{7}", name, err)
		}
	}
	if got := a.output(); got != "early livebye" {
		t.Fatalf("client A saw %q", got)
	}

	ts, err := readSessionFile[Tombstone](opts.TombstonePath)
	if err != nil || ts.ExitCode != 7 || ts.LaunchToken != opts.LaunchToken {
		t.Fatalf("tombstone = %+v, %v", ts, err)
	}
	if _, err := readSessionFile[Marker](opts.MarkerPath); !os.IsNotExist(err) {
		t.Fatalf("marker still present after clean exit: %v", err)
	}
	if _, err := os.Stat(opts.AttachSocket); !os.IsNotExist(err) {
		t.Fatalf("attach socket not removed: %v", err)
	}
}

func TestHolderReplayRestoresEvictedTerminalInputModes(t *testing.T) {
	opts, _ := testPaths(t)
	opts.RingBytes = 16
	fs := startFakeSandbox(t, opts.SSHSocket)
	res := runHolderAsync(context.Background(), opts)
	<-fs.started

	original := attachWhenReady(t, opts.AttachSocket)
	if _, err := original.stdinW.Write([]byte("ready")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "original client registration", func() bool {
		fs.mu.Lock()
		defer fs.mu.Unlock()
		return fs.stdin.String() == "ready"
	})

	// Evict startup keyboard/mouse/paste negotiation, then stop mid-push. A
	// reattach must restore the earlier stack without interrupting this CSI.
	fs.emit("\x1b[>3u\x1b[?1003h\x1b[?1006h\x1b[?2004h")
	const tail = "01234567890123456789\x1b[>31"
	fs.emit(tail)
	waitFor(t, "live output at original client", func() bool {
		return strings.HasSuffix(original.output(), "01234567890123456789")
	})

	reattached := attach(t, context.Background(), opts.AttachSocket)
	waitFor(t, "replay with terminal input state", func() bool {
		out := reattached.output()
		return strings.HasSuffix(out, "4567890123456789")
	})
	liveVT, replayVT := replayTestTerminal(t), replayTestTerminal(t)
	writeReplayTerminal(t, liveVT, original.output())
	writeReplayTerminal(t, replayVT, reattached.output())
	checkReplayInput(t, liveVT, replayVT)
	if replayVT.Get(8) != 3 || !replayVT.Mode(2004) || !replayVT.Mode(1003) {
		t.Fatal("startup input negotiation was lost")
	}
	key, err := replayVT.EncodeKey(terminal.Key{Name: "enter", Codepoint: '\r', Mods: 1, Action: 1})
	if err != nil || string(key) != "\x1b[13;2u" {
		t.Fatalf("reattached Shift+Enter = %q, %v", key, err)
	}
	if _, err := reattached.stdinW.Write(key); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "reattached key delivered to agent", func() bool {
		fs.mu.Lock()
		defer fs.mu.Unlock()
		return fs.stdin.String() == "ready\x1b[13;2u"
	})
	// Complete the split push, then pop it: the underlying flags must still
	// be 3. A second pop must reach the original zero-flag baseline.
	for _, step := range []struct {
		output string
		flags  int
	}{
		{"\x1b[>31u\x1b[<u", 3},
		{"\x1b[<1u", 0},
	} {
		if strings.HasPrefix(step.output, "\x1b[>31") {
			fs.emit(strings.TrimPrefix(step.output, "\x1b[>31"))
		} else {
			fs.emit(step.output)
		}
		waitFor(t, "live stack continuation", func() bool {
			return strings.HasSuffix(reattached.output(), step.output)
		})
		writeReplayTerminal(t, liveVT, step.output)
		writeReplayTerminal(t, replayVT, step.output)
		checkReplayInput(t, liveVT, replayVT)
		if replayVT.Get(8) != step.flags {
			t.Fatalf("flags after %q = %d, want %d", step.output, replayVT.Get(8), step.flags)
		}
	}

	fs.exit(0)
	if r := <-res; r.err != nil || r.status != 0 {
		t.Fatalf("RunHolder = %d, %v; want 0, nil", r.status, r.err)
	}
	for name, client := range map[string]*attachClient{
		"original":   original,
		"reattached": reattached,
	} {
		if err := <-client.done; err != nil {
			t.Fatalf("%s Attach = %v, want nil", name, err)
		}
	}
}

func TestHolderReplayOmitsOSC52ClipboardWrites(t *testing.T) {
	opts, _ := testPaths(t)
	fs := startFakeSandbox(t, opts.SSHSocket)
	res := runHolderAsync(context.Background(), opts)
	<-fs.started

	live := attachWhenReady(t, opts.AttachSocket)
	if _, err := live.stdinW.Write([]byte("ready")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "live client registration", func() bool {
		fs.mu.Lock()
		defer fs.mu.Unlock()
		return fs.stdin.String() == "ready"
	})

	// An already-attached client executes the copy live, verbatim.
	const copySeq = "\x1b]52;c;aGVsbG8=\x1b\\"
	fs.emit("before " + copySeq + "after")
	waitFor(t, "verbatim copy sequence at the live client", func() bool {
		return live.output() == "before "+copySeq+"after"
	})

	// A re-attach must not re-execute it: the replay keeps the surrounding
	// output but the clipboard write is gone from the ring.
	reattached := attach(t, context.Background(), opts.AttachSocket)
	waitFor(t, "replay without the copy sequence", func() bool {
		return reattached.output() == "before after"
	})

	fs.exit(0)
	if r := <-res; r.err != nil || r.status != 0 {
		t.Fatalf("RunHolder = %d, %v; want 0, nil", r.status, r.err)
	}
	for name, client := range map[string]*attachClient{
		"live":       live,
		"reattached": reattached,
	} {
		if err := <-client.done; err != nil {
			t.Fatalf("%s Attach = %v, want nil", name, err)
		}
	}
}

func TestHolderBellsAreLiveOnly(t *testing.T) {
	opts, _ := testPaths(t)
	opts.TitleTemplate = "{title}"
	fs := startFakeSandbox(t, opts.SSHSocket)
	res := runHolderAsync(context.Background(), opts)
	<-fs.started
	live := attachWhenReady(t, opts.AttachSocket)
	_, _ = live.stdinW.Write([]byte("ready"))
	waitFor(t, "live bell client", func() bool {
		fs.mu.Lock()
		defer fs.mu.Unlock()
		return fs.stdin.String() == "ready"
	})
	const first = "before\a\x1b]2;Ü title\x1b\\after"
	fs.emit(first)
	waitFor(t, "live bell", func() bool { return live.output() == first })
	reattached := attach(t, context.Background(), opts.AttachSocket)
	const replay = "before\x00\x1b]2;Ü title\x1b\\after"
	waitFor(t, "quiet replay with intact Unicode title", func() bool { return reattached.output() == replay })
	// The replay filter must not suppress a new bell for either live client.
	fs.emit("\aend")
	waitFor(t, "new bell on both clients", func() bool {
		return live.output() == first+"\aend" && reattached.output() == replay+"\aend"
	})
	fs.exit(0)
	if r := <-res; r.err != nil || r.status != 0 {
		t.Fatalf("RunHolder = %d, %v", r.status, r.err)
	}
	for _, client := range []*attachClient{live, reattached} {
		if err := <-client.done; err != nil {
			t.Fatal(err)
		}
	}
}

func TestHolderReplayKeepsMidSequenceAttachContinuous(t *testing.T) {
	opts, _ := testPaths(t)
	fs := startFakeSandbox(t, opts.SSHSocket)
	res := runHolderAsync(context.Background(), opts)
	<-fs.started

	live := attachWhenReady(t, opts.AttachSocket)
	if _, err := live.stdinW.Write([]byte("a")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "live client registration", func() bool {
		fs.mu.Lock()
		defer fs.mu.Unlock()
		return fs.stdin.String() == "a"
	})

	// Stop mid-OSC 52: the opening bytes are withheld from the ring.
	const opening = "x\x1b]52;c;aGVsbG8="
	fs.emit(opening)
	waitFor(t, "opening bytes at the live client", func() bool {
		return live.output() == "x"
	})

	// A client attaching now must still get those opening bytes (from the
	// filter's pending buffer) so the live continuation completes a valid
	// sequence — this copy is happening now, not being replayed.
	mid := attach(t, context.Background(), opts.AttachSocket)
	if _, err := mid.stdinW.Write([]byte("b")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "mid client registration", func() bool {
		fs.mu.Lock()
		defer fs.mu.Unlock()
		return fs.stdin.String() == "ab"
	})

	fs.emit("\x1b\\rest")
	waitFor(t, "completed sequence at the mid-attach client", func() bool {
		return mid.output() == opening+"\x1b\\rest"
	})

	// Once completed, the sequence is gone from the ring for later attaches.
	late := attach(t, context.Background(), opts.AttachSocket)
	waitFor(t, "late replay without the copy sequence", func() bool {
		return late.output() == "xrest"
	})

	fs.exit(0)
	if r := <-res; r.err != nil || r.status != 0 {
		t.Fatalf("RunHolder = %d, %v; want 0, nil", r.status, r.err)
	}
	for name, client := range map[string]*attachClient{
		"live": live,
		"mid":  mid,
		"late": late,
	} {
		if err := <-client.done; err != nil {
			t.Fatalf("%s Attach = %v, want nil", name, err)
		}
	}
}

func TestHolderCleanExitZero(t *testing.T) {
	opts, _ := testPaths(t)
	fs := startFakeSandbox(t, opts.SSHSocket)
	res := runHolderAsync(context.Background(), opts)
	<-fs.started
	fs.exit(0)
	r := <-res
	if r.err != nil || r.status != 0 {
		t.Fatalf("RunHolder = %d, %v; want 0, nil", r.status, r.err)
	}
	ts, err := readSessionFile[Tombstone](opts.TombstonePath)
	if err != nil || ts.ExitCode != 0 {
		t.Fatalf("tombstone = %+v, %v", ts, err)
	}
}

func TestHolderStopLeavesAgentRunning(t *testing.T) {
	opts, _ := testPaths(t)
	fs := startFakeSandbox(t, opts.SSHSocket)
	ctx, cancel := context.WithCancel(context.Background())
	res := runHolderAsync(ctx, opts)
	<-fs.started

	// Unit stop: context canceled. Per spike F1 the holder must NOT try
	// to terminate the agent — it detaches and leaves the marker.
	cancel()
	r := <-res
	if !errors.Is(r.err, context.Canceled) {
		t.Fatalf("RunHolder err = %v, want context.Canceled", r.err)
	}
	if _, err := readSessionFile[Marker](opts.MarkerPath); err != nil {
		t.Fatalf("marker must survive a holder stop: %v", err)
	}
	if _, err := readSessionFile[Tombstone](opts.TombstonePath); !os.IsNotExist(err) {
		t.Fatalf("no tombstone expected on stop, got err=%v", err)
	}
}

func TestHolderStatuslessCloseKeepsMarker(t *testing.T) {
	opts, _ := testPaths(t)
	fs := startFakeSandbox(t, opts.SSHSocket)
	res := runHolderAsync(context.Background(), opts)
	<-fs.started

	a := attachWhenReady(t, opts.AttachSocket)
	// A successful dial only queues the connection. Wait until the holder has
	// registered the client (stdin flows through its read loop, which starts
	// after registration) so the close below reaches an attached client.
	if _, err := a.stdinW.Write([]byte("ready")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "client registration", func() bool {
		fs.mu.Lock()
		defer fs.mu.Unlock()
		return fs.stdin.String() == "ready"
	})

	fs.closeStatusless()
	r := <-res
	if !errors.Is(r.err, ErrHolderConnLost) {
		t.Fatalf("RunHolder err = %v, want ErrHolderConnLost", r.err)
	}
	// Possible orphan: marker stays, no tombstone (spike F2).
	if _, err := readSessionFile[Marker](opts.MarkerPath); err != nil {
		t.Fatalf("marker must survive a statusless close: %v", err)
	}
	if _, err := readSessionFile[Tombstone](opts.TombstonePath); !os.IsNotExist(err) {
		t.Fatalf("no tombstone expected, got err=%v", err)
	}
	// The attached client sees the stream end without an exit status.
	if err := a.wait(t); !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("client attach result = %v, want ErrSessionClosed", err)
	}
}

// A client the accept loop picks up after closeClients has swept must still
// be served and released: HELLO, the replay, then the ending the swept
// clients got. Registering it leaks the connection, because nothing closes it
// afterwards. A deployed holder masks the leak by exiting; in process both
// ends stay blocked in reads, which is how
// TestHolderStatuslessCloseKeepsMarker used to hang.
func TestHolderReleasesClientAcceptedAfterClose(t *testing.T) {
	for _, tc := range []struct {
		name        string
		exitPayload []byte // what closeClients was called with
		want        error  // what the late client's Attach must return
	}{
		{name: "statusless close", want: ErrSessionClosed},
		{name: "clean exit", exitPayload: encodeExit(0)},
		{name: "nonzero exit", exitPayload: encodeExit(7), want: &ExitError{Status: 7}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts, _ := testPaths(t)
			ln, err := net.Listen("unix", opts.AttachSocket)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = ln.Close() })

			h := &holder{
				ring:          NewRing(opts.RingBytes),
				terminalInput: &terminalInputState{},
				logf:          t.Logf,
			}
			if err := h.initView(24, 80); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = h.vt.Close() })
			t.Cleanup(h.terminalInput.Close)
			h.broadcast([]byte("final screen"))
			h.closeClients(tc.exitPayload)
			go h.acceptLoop(ln)

			late := attach(t, context.Background(), opts.AttachSocket)
			got := late.wait(t)
			ok := errors.Is(got, tc.want)
			var gotExit, wantExit *ExitError
			if errors.As(tc.want, &wantExit) {
				ok = errors.As(got, &gotExit) && gotExit.Status == wantExit.Status
			}
			if !ok {
				t.Fatalf("late client attach result = %v, want %v", got, tc.want)
			}
			if out := late.output(); out != "final screen" {
				t.Fatalf("late client saw %q, want the replay", out)
			}
		})
	}
}

// attachWhenReady attaches as soon as the holder accepts connections. It
// retries the dial instead of waiting for the socket path to exist: the
// path appears at bind(), before listen(), and a dial in between is refused.
func attachWhenReady(t *testing.T, sock string) *attachClient {
	t.Helper()
	var conn net.Conn
	waitFor(t, "attach socket", func() bool {
		c, err := net.Dial("unix", sock)
		if err != nil {
			return false
		}
		conn = c
		return true
	})
	return attachConn(context.Background(), conn)
}

func TestHolderRefusesIncompleteOptions(t *testing.T) {
	if _, err := RunHolder(context.Background(), HolderOptions{Agentbox: "x"}); err == nil {
		t.Fatal("incomplete options accepted")
	}
}

func TestUnitNameShape(t *testing.T) {
	if got, want := UnitName("coder"), "yoloctl-session-coder.service"; got != want {
		t.Fatalf("UnitName = %q, want %q", got, want)
	}
	if SocketPath("coder") != SocketDir+"/coder.sock" {
		t.Fatalf("SocketPath = %q", SocketPath("coder"))
	}
	for _, p := range []string{MarkerPath("coder"), TombstonePath("coder")} {
		if !filepath.IsAbs(p) || !bytes.Contains([]byte(p), []byte("/sessions/coder/")) {
			t.Fatalf("session state path %q not under the sessions tree", p)
		}
	}
}

func TestHolderCancellationInterruptsSilentSSHHandshake(t *testing.T) {
	opts, _ := testPaths(t)
	ln, err := net.Listen("unix", opts.SSHSocket)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := runHolderAsync(ctx, opts)
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			accepted <- c
		}
	}()
	select {
	case c := <-accepted:
		defer func() { _ = c.Close() }()
		// A peer that never sends its SSH version must not strand teardown.
		cancel()
	case <-time.After(5 * time.Second):
		t.Fatal("holder did not connect")
	}
	select {
	case got := <-result:
		if got.err == nil {
			t.Fatal("silent SSH endpoint accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("holder ignored cancellation during handshake")
	}
	if _, err := os.Stat(opts.MarkerPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("handshake failure published an agent marker: %v", err)
	}
}
