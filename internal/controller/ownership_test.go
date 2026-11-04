package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/leogr/yoloctl/internal/termio"
)

const (
	ownedClientEnv  = "YOLOCTL_TEST_OWNED_CLIENT"
	ownedRequestEnv = "YOLOCTL_TEST_OWNED_REQUEST"
	// Each worker stream writes beyond the retained-output ring after detach.
	ownedChunk  = 64 << 10
	ownedChunks = 5
)

// ownedAttachPolicy is the production box.attach registration; the cmd
// package asserts the same value.
var ownedAttachPolicy = Operation{BoxAllowed: true, Mutates: true, EffectGated: true, OwnedSetup: true}

type ownedResult struct {
	Stdout, Stderr, View int
	WriteErrors          []string
	Signals              []string
	EarlyInput           int
	Release              string
	Echoed               int
}

// ownedSetupWorker is a box.attach worker whose authorized setup waits at a
// start barrier (before its countable holder start) and a handoff barrier.
// It records every SIGINT, SIGHUP and SIGPIPE instead of dying from it.
func ownedSetupWorker(released *os.File, req Request) {
	action, dir := req.Args[0], req.Args[1]
	caught := make(chan os.Signal, 16)
	signal.Notify(caught, syscall.SIGINT, syscall.SIGHUP, syscall.SIGPIPE)
	checkpoints := NewWorkerCheckpoints(released)
	exit := func(code int) { _ = checkpoints.Finish(); os.Exit(code) }
	var view *os.File
	if req.View {
		var err error
		if view, err = termio.ViewFile(5); err != nil {
			exit(97)
		}
	}
	stdin := os.Stdin
	if !req.Terminal {
		// A nonblocking descriptor supports the read deadline below.
		if err := syscall.SetNonblock(0, true); err != nil {
			exit(96)
		}
		stdin = os.NewFile(0, "stdin")
	}
	if err := checkpoints.BeginEffects(context.Background(), EffectIntent{}); err != nil {
		exit(2)
	}
	_ = os.WriteFile(filepath.Join(dir, "arrived"), nil, 0o600)
	_, _ = os.Stdout.Write([]byte("setup-started\n"))
	awaitFile(filepath.Join(dir, "start"))
	if action == "owned-kill" {
		_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
	}
	var result ownedResult
	record := func(total *int, n int, err error) {
		*total += n
		if err != nil {
			result.WriteErrors = append(result.WriteErrors, err.Error())
		}
	}
	chunk := bytes.Repeat([]byte("x"), ownedChunk)
	for range ownedChunks {
		n, err := os.Stdout.Write(chunk)
		record(&result.Stdout, n, err)
		n, err = os.Stderr.Write(chunk)
		record(&result.Stderr, n, err)
		if view != nil {
			n, err = view.Write(chunk)
			record(&result.View, n, err)
		}
	}
	effect, err := os.OpenFile(filepath.Join(dir, "holder"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		exit(2)
	}
	_, _ = effect.Write([]byte("holder\n"))
	_ = effect.Close()
	finish := func(code int) {
		time.Sleep(50 * time.Millisecond) // let any pending signal arrive
		for done := false; !done; {
			select {
			case sig := <-caught:
				result.Signals = append(result.Signals, sig.String())
			default:
				done = true
			}
		}
		raw, _ := json.Marshal(result)
		_ = os.WriteFile(filepath.Join(dir, "result.tmp"), raw, 0o600)
		_ = os.Rename(filepath.Join(dir, "result.tmp"), filepath.Join(dir, "result"))
		exit(code)
	}
	if action == "owned-fail" {
		finish(5)
	}
	awaitFile(filepath.Join(dir, "handoff"))
	if !req.Terminal {
		// Held client input must not reach the worker before handoff.
		if err := stdin.SetReadDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
			exit(95)
		}
		early := make([]byte, 64)
		result.EarlyInput, _ = stdin.Read(early)
		_ = stdin.SetReadDeadline(time.Time{})
	}
	switch err := checkpoints.Release(); {
	case err == nil:
		result.Release = "attached"
	case errors.Is(err, ErrClientDetached):
		result.Release = "detached"
		finish(0)
	default:
		result.Release = err.Error()
		finish(3)
	}
	n, _ := io.Copy(os.Stdout, stdin)
	result.Echoed = int(n)
	finish(17)
}

func awaitFile(path string) {
	for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if _, err := os.Stat(path); err == nil {
			return
		}
	}
	os.Exit(90)
}

// ownedSetupClient is a real client process that a test terminates or whose
// stdout consumer exits; it relays like the box-use client.
func ownedSetupClient(socket string) {
	var req Request
	if err := json.Unmarshal([]byte(os.Getenv(ownedRequestEnv)), &req); err != nil {
		os.Exit(80)
	}
	conn, err := net.Dial("unix", socket)
	if err != nil {
		os.Exit(81)
	}
	local, remote, err := termio.ViewPair()
	if err != nil {
		os.Exit(82)
	}
	go func() { _, _ = io.Copy(io.Discard, local) }()
	if err := Call(context.Background(), conn, hello(), req, ClientIO{Out: os.Stdout, Err: os.Stderr, View: remote}); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

// closeNotifyConn reports when ServeConn has closed the public connection,
// which it does exactly when it observes the client's departure.
type closeNotifyConn struct {
	net.Conn
	once   sync.Once
	closed chan struct{}
}

func (c *closeNotifyConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return c.Conn.Close()
}

var ownedSockets atomic.Int64

type ownedHarness struct {
	t         *testing.T
	s         *Server
	socket    string
	dir       string
	served    chan struct{}
	detached  chan struct{}
	principal Principal
}

func newOwnedHarness(t *testing.T, principal Principal) *ownedHarness {
	t.Helper()
	h := &ownedHarness{t: t, dir: t.TempDir(), served: make(chan struct{}), detached: make(chan struct{}), principal: principal,
		socket: fmt.Sprintf("@yoloctl-owned-%d-%d", os.Getpid(), ownedSockets.Add(1))}
	h.s = &Server{Hello: hello(), Receipts: Receipts{t.TempDir()}, Execute: RunWorker,
		Operations:     map[string]Operation{"box.attach": ownedAttachPolicy, "test.gated": execPolicy},
		CheckPrincipal: func(context.Context, Principal) error { return nil }}
	listener, err := net.Listen("unix", h.socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		defer close(h.served)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		h.s.ServeConn(context.Background(), &closeNotifyConn{Conn: conn, closed: h.detached}, h.principal)
	}()
	return h
}

func (h *ownedHarness) request(action string, terminal bool) Request {
	req := request("box.attach")
	req.Args = []string{action, h.dir}
	req.View = true
	req.Terminal = terminal
	req.Cols, req.Rows, req.Term = 80, 24, "xterm-256color"
	return req
}

func (h *ownedHarness) await(name string) {
	h.t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if _, err := os.Stat(filepath.Join(h.dir, name)); err == nil {
			return
		}
		select {
		case <-h.served:
			r, err := h.s.Receipts.Read(request("box.attach").ID)
			h.t.Fatalf("request ended before the worker reached %s: %+v %v", name, r, err)
		default:
		}
	}
	h.t.Fatalf("worker never reached %s", name)
}

func (h *ownedHarness) wait(ch <-chan struct{}, what string) {
	h.t.Helper()
	select {
	case <-ch:
	case <-time.After(15 * time.Second):
		h.t.Fatalf("timed out waiting for %s", what)
	}
}

// releaseBarriers lets the owned setup finish only after its client is gone.
func (h *ownedHarness) releaseBarriers() {
	h.t.Helper()
	h.wait(h.detached, "the controller to observe the client's departure")
	for _, name := range []string{"start", "handoff"} {
		if err := os.WriteFile(filepath.Join(h.dir, name), nil, 0o600); err != nil {
			h.t.Fatal(err)
		}
	}
	h.wait(h.served, "the owned setup to finish")
}

func (h *ownedHarness) result() ownedResult {
	h.t.Helper()
	var r ownedResult
	raw, err := os.ReadFile(filepath.Join(h.dir, "result"))
	if err != nil || json.Unmarshal(raw, &r) != nil {
		h.t.Fatal("worker recorded no result", err)
	}
	return r
}

func (h *ownedHarness) holders() int {
	raw, err := os.ReadFile(filepath.Join(h.dir, "holder"))
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		h.t.Fatal(err)
	}
	return strings.Count(string(raw), "holder\n")
}

// assertNextMutation proves the space is not fenced: another box's effect-
// gated mutation runs without any acknowledgment.
func (h *ownedHarness) assertNextMutation() {
	h.t.Helper()
	next := request("test.gated")
	next.ID += "-next"
	next.Args = []string{"normal", filepath.Join(h.t.TempDir(), "other-box")}
	var exit *ExitError
	if err := invoke(h.t, h.s, next, nil, io.Discard, io.Discard); !errors.As(err, &exit) || exit.Status != 7 {
		h.t.Fatal("another box mutation was refused", err)
	}
	if r, err := h.s.Receipts.Read(next.ID); err != nil || r.State != "complete" {
		h.t.Fatal(r, err)
	}
}

// assertOwnedSurvived is A3's oracle: one setup finished, every post-detach
// write succeeded, no SIGINT/SIGHUP/SIGPIPE reached the worker, the receipt
// settled without uncertainty and the space still accepts mutations.
func (h *ownedHarness) assertOwnedSurvived(req Request, detachSignal string) {
	h.t.Helper()
	if n := h.holders(); n != 1 {
		h.t.Fatalf("setup finished %d times", n)
	}
	got := h.result()
	if len(got.WriteErrors) != 0 || len(got.Signals) != 0 || got.EarlyInput != 0 || got.Release != "detached" {
		h.t.Fatalf("worker observed client loss: %+v", got)
	}
	if got.Stdout != ownedChunk*ownedChunks || !req.Terminal && got.Stderr != ownedChunk*ownedChunks || got.View != ownedChunk*ownedChunks {
		h.t.Fatalf("post-detach writes were short: %+v", got)
	}
	r, err := h.s.Receipts.Read(req.ID)
	if err != nil || r.State != "complete" || r.Status != 0 || !r.SetupComplete || r.EffectPhase != EffectStarted || !r.ClientDetached || r.DetachSignal != detachSignal || h.s.RecoveryRequired {
		h.t.Fatalf("receipt=%+v recovery=%v err=%v", r, h.s.RecoveryRequired, err)
	}
	h.assertNextMutation()
}

func ownedPrincipals() map[string]Principal {
	return map[string]Principal{"admin": {}, "operator": {Box: "caller", Incarnation: "live"}}
}

// startOwnedClient runs the relaying client as its own process.
func (h *ownedHarness) startOwnedClient(req Request, stdout *os.File) *exec.Cmd {
	h.t.Helper()
	raw, _ := json.Marshal(req)
	executable, err := os.Executable()
	if err != nil {
		h.t.Fatal(err)
	}
	cmd := exec.Command(executable)
	cmd.Env = append(os.Environ(), ownedClientEnv+"="+h.socket, ownedRequestEnv+"="+string(raw))
	cmd.Stdout, cmd.Stderr = stdout, nil
	if err := cmd.Start(); err != nil {
		h.t.Fatal(err)
	}
	return cmd
}

func signaledBy(t *testing.T, err error, want syscall.Signal) {
	t.Helper()
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("client exit %v, want %v", err, want)
	}
	status, ok := exit.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != want {
		t.Fatalf("client exit %v, want %v", err, want)
	}
}

// A3: both incident triggers (a terminated client and a closed output
// consumer) plus every other way a client can vanish during owned setup.
func TestOwnedAttachSetupSurvivesClientProcessLoss(t *testing.T) {
	for name, principal := range ownedPrincipals() {
		for _, trigger := range []string{"SIGTERM", "SIGKILL", "closed stdout"} {
			t.Run(name+"/"+trigger, func(t *testing.T) {
				h := newOwnedHarness(t, principal)
				req := h.request("owned", false)
				stdout, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = stdout.Close() }()
				if trigger == "closed stdout" {
					// A real consumer that exits at once, like the second incident.
					read, write, err := os.Pipe()
					if err != nil {
						t.Fatal(err)
					}
					consumer := exec.Command("true") // /usr/bin/true on macOS
					consumer.Stdin = read
					if err := consumer.Run(); err != nil {
						t.Fatal(err)
					}
					_ = read.Close()
					defer func() { _ = write.Close() }()
					stdout = write
				}
				client := h.startOwnedClient(req, stdout)
				h.await("arrived")
				switch trigger {
				case "SIGTERM":
					_ = client.Process.Signal(syscall.SIGTERM)
				case "SIGKILL":
					_ = client.Process.Kill()
				}
				err = client.Wait()
				signaledBy(t, err, map[string]syscall.Signal{"SIGTERM": syscall.SIGTERM, "SIGKILL": syscall.SIGKILL, "closed stdout": syscall.SIGPIPE}[trigger])
				h.releaseBarriers()
				h.assertOwnedSurvived(req, "")
			})
		}
	}
}

// rawOwnedClient speaks the public protocol directly to send frames that the
// relaying client never produces.
func (h *ownedHarness) rawOwnedClient(req Request) (*Wire, net.Conn) {
	h.t.Helper()
	conn, err := net.Dial("unix", h.socket)
	if err != nil {
		h.t.Fatal(err)
	}
	wire := NewWire(conn, conn)
	hi := hello()
	if err := wire.Write(Frame{Type: "hello", Hello: &hi}); err != nil {
		h.t.Fatal(err)
	}
	if f, err := wire.Read(); err != nil || f.Type != "hello" {
		h.t.Fatal("handshake", f, err)
	}
	if err := wire.Write(Frame{Type: "request", Request: &req}); err != nil {
		h.t.Fatal(err)
	}
	return wire, conn
}

func TestOwnedAttachSetupSurvivesClientFrames(t *testing.T) {
	cases := []struct {
		name     string
		terminal bool
		frames   []Frame
		// detachSignal is the signal frame that must end the client.
		detachSignal string
	}{
		{name: "cancel", frames: []Frame{{Type: "cancel"}}},
		{name: "signal", frames: []Frame{{Type: "signal", Signal: 2}, {Type: "signal", Signal: 15}}, detachSignal: "SIGINT"},
		{name: "malformed", frames: []Frame{{Type: "bogus"}}},
		{name: "overflowed input", frames: []Frame{{Type: "stdin", Data: bytes.Repeat([]byte{'y'}, 65537)}}},
		{name: "flooded input", frames: func() []Frame {
			frames := make([]Frame, 10)
			for i := range frames {
				frames[i] = Frame{Type: "stdin", Data: []byte("typeahead\x03")}
			}
			return frames
		}()},
		// The PTY case needs /dev/ptmx: the queued Ctrl-C byte must never
		// reach the line discipline of the setup worker.
		{name: "PTY ctrl-c", terminal: true, frames: []Frame{{Type: "stdin", Data: []byte{0x03}}, {Type: "signal", Signal: 2}}, detachSignal: "SIGINT"},
	}
	for name, principal := range ownedPrincipals() {
		for _, tc := range cases {
			t.Run(name+"/"+tc.name, func(t *testing.T) {
				if tc.terminal {
					master, slave, err := pty.Open()
					if err != nil {
						t.Fatal("this case requires a PTY:", err)
					}
					_ = master.Close()
					_ = slave.Close()
				}
				h := newOwnedHarness(t, principal)
				req := h.request("owned", tc.terminal)
				wire, conn := h.rawOwnedClient(req)
				h.await("arrived")
				for _, f := range tc.frames {
					if err := wire.Write(f); err != nil {
						break // the controller may already have detached this client
					}
				}
				if tc.detachSignal != "" {
					// The signal ends the client explicitly; setup continues.
					if f := readUntil(t, wire, "detached"); !f.Owned || f.Signal != 2 {
						t.Fatalf("detached frame %+v", f)
					}
				}
				// Frames sent before closing are read before the EOF.
				_ = conn.Close()
				h.releaseBarriers()
				h.assertOwnedSurvived(req, tc.detachSignal)
			})
		}
	}
}

// readUntil skips worker output frames until the wanted frame type.
func readUntil(t *testing.T, wire *Wire, want string) Frame {
	t.Helper()
	for {
		f, err := wire.Read()
		if err != nil {
			t.Fatalf("connection ended before a %s frame: %v", want, err)
		}
		switch f.Type {
		case want:
			return f
		case "stdout", "stderr", "view", "input-ready", "view-ready":
		default:
			t.Fatalf("unexpected frame %+v while waiting for %s", f, want)
		}
	}
}

// A signal before authorization ends the client and refuses effects, even
// when the gate runs before the connection's cancellation is observed.
func TestWaitingAttachSignalDetachesWithoutEffects(t *testing.T) {
	signalled := make(chan struct{})
	var once sync.Once
	previousGate := beforeEffectGate
	t.Cleanup(func() { beforeEffectGate = previousGate })
	beforeEffectGate = func(context.Context) { once.Do(func() { <-signalled }) }
	h := newOwnedHarness(t, Principal{})
	req := h.request("owned", false)
	wire, conn := h.rawOwnedClient(req)
	defer func() { _ = conn.Close() }()
	if err := wire.Write(Frame{Type: "signal", Signal: 15}); err != nil {
		t.Fatal(err)
	}
	if f := readUntil(t, wire, "detached"); f.Owned || f.Signal != 15 {
		t.Fatalf("detached frame %+v", f)
	}
	close(signalled)
	h.wait(h.served, "the refused setup to end")
	r, err := h.s.Receipts.Read(req.ID)
	if err != nil || r.State != "complete" || r.Status == 0 || r.EffectPhase != EffectWaiting || r.SetupComplete || r.DetachSignal != "SIGTERM" || !r.ClientDetached || h.s.RecoveryRequired {
		t.Fatal(r, h.s.RecoveryRequired, err)
	}
	if _, err := os.Stat(filepath.Join(h.dir, "arrived")); !errors.Is(err, os.ErrNotExist) || h.holders() != 0 {
		t.Fatal("a refused setup executed an effect")
	}
	h.assertNextMutation()
}

func TestSetupOwnerSignalRefusesLaterAuthorization(t *testing.T) {
	conn, cancelConn := context.WithCancel(context.Background())
	defer cancelConn()
	exec, cancelExec := context.WithCancel(context.Background())
	defer cancelExec()
	o := newSetupOwner(true, conn, cancelExec, &clientOutput{})
	if forward, owned := o.signal(1); forward || owned {
		t.Fatal(forward, owned)
	}
	saved := false
	if err := o.authorize(exec, func() error { saved = true; return nil }); !errors.Is(err, ErrClientDetached) || saved {
		t.Fatal(err, saved)
	}
	if detached, sig := o.outcome(); !detached || sig != 1 {
		t.Fatal(detached, sig)
	}
	// After handoff a signal reaches the worker.
	o = newSetupOwner(true, conn, cancelExec, &clientOutput{})
	if err := o.authorize(exec, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if attached, err := o.handoff(func() error { return nil }); !attached || err != nil {
		t.Fatal(attached, err)
	}
	if forward, _ := o.signal(15); !forward {
		t.Fatal("a signal after handoff was not forwarded")
	}
	if detached, sig := o.outcome(); detached || sig != 0 {
		t.Fatal(detached, sig)
	}
}

// After handoff the first signal reaches the attached worker, as before.
func TestOwnedAttachForwardsSignalAfterHandoff(t *testing.T) {
	h := newOwnedHarness(t, Principal{})
	req := h.request("owned", false)
	wire, conn := h.rawOwnedClient(req)
	defer func() { _ = conn.Close() }()
	h.await("arrived")
	for _, name := range []string{"start", "handoff"} {
		if err := os.WriteFile(filepath.Join(h.dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Held input is echoed only once the setup handed off.
	if err := wire.Write(Frame{Type: "stdin", Data: []byte("handed-off")}); err != nil {
		t.Fatal(err)
	}
	for echoed := ""; !strings.Contains(echoed, "handed-off"); {
		echoed += string(readUntil(t, wire, "stdout").Data)
	}
	if err := wire.Write(Frame{Type: "signal", Signal: 1}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond) // let the worker's group receive it
	if err := wire.Write(Frame{Type: "eof"}); err != nil {
		t.Fatal(err)
	}
	if f := readUntil(t, wire, "exit"); f.Status != 17 {
		t.Fatalf("exit %+v", f)
	}
	h.wait(h.served, "the attachment to end")
	if got := h.result(); got.Release != "attached" || len(got.Signals) != 1 || got.Signals[0] != syscall.SIGHUP.String() {
		t.Fatalf("worker result %+v", got)
	}
	if r, err := h.s.Receipts.Read(req.ID); err != nil || r.State != "complete" || r.Status != 17 || r.ClientDetached || r.DetachSignal != "" || h.s.RecoveryRequired {
		t.Fatal(r, h.s.RecoveryRequired, err)
	}
}

// A2: the detach and the effect permission are serialized by one mutex.
func TestOwnedAttachPermissionRace(t *testing.T) {
	for _, winner := range []string{"detach", "owner"} {
		t.Run(winner+" first", func(t *testing.T) {
			// The hooks are installed before the request; the client's
			// connection reaches them through a channel.
			conns := make(chan net.Conn, 1)
			var h *ownedHarness
			detached := make(chan (<-chan struct{}), 1)
			var once sync.Once
			hook := func(connCtx context.Context) {
				once.Do(func() { // the follow-up mutation passes the gate untouched
					_ = (<-conns).Close()
					<-connCtx.Done()
					<-<-detached
				})
			}
			previousGate, previousPermit := beforeEffectGate, beforeEffectPermit
			t.Cleanup(func() { beforeEffectGate, beforeEffectPermit = previousGate, previousPermit })
			if winner == "detach" {
				beforeEffectGate = hook
			} else {
				// Between the durable started save and the permit byte.
				beforeEffectPermit = hook
			}
			h = newOwnedHarness(t, Principal{})
			detached <- h.detached
			req := h.request("owned", false)
			_, conn := h.rawOwnedClient(req)
			conns <- conn
			if winner == "detach" {
				h.wait(h.served, "the denied setup to end")
				r, err := h.s.Receipts.Read(req.ID)
				if err != nil || r.State != "complete" || r.Status == 0 || r.EffectPhase != EffectWaiting || r.SetupComplete || h.s.RecoveryRequired {
					t.Fatal(r, h.s.RecoveryRequired, err)
				}
				if _, err := os.Stat(filepath.Join(h.dir, "arrived")); !errors.Is(err, os.ErrNotExist) || h.holders() != 0 {
					t.Fatal("a denied setup executed an effect")
				}
				h.assertNextMutation()
				return
			}
			h.await("arrived")
			h.releaseBarriers()
			h.assertOwnedSurvived(req, "")
		})
	}
}

// A4: a connected client keeps typeahead order across handoff; its input is
// held, not delivered to the setup worker.
func TestOwnedAttachConnectedClientKeepsTypeaheadOrder(t *testing.T) {
	h := newOwnedHarness(t, Principal{Box: "caller", Incarnation: "live"})
	before := openDescriptors(t)
	req := h.request("owned", false)
	conn, err := net.Dial("unix", h.socket)
	if err != nil {
		t.Fatal(err)
	}
	local, remote, err := termio.ViewPair()
	if err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = io.Copy(io.Discard, local) }()
	typeahead := "first\x00second\r\n\x03third"
	var out bytes.Buffer
	called := make(chan error, 1)
	go func() {
		called <- Call(context.Background(), conn, hello(), req, ClientIO{In: strings.NewReader(typeahead), Out: &out, Err: io.Discard, View: remote})
	}()
	h.await("arrived")
	for _, name := range []string{"start", "handoff"} {
		if err := os.WriteFile(filepath.Join(h.dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var exit *ExitError
	if err := <-called; !errors.As(err, &exit) || exit.Status != 17 {
		t.Fatal("attached setup did not stream to its end", err)
	}
	h.wait(h.served, "the attachment to end")
	got := h.result()
	if got.EarlyInput != 0 || got.Release != "attached" || got.Echoed != len(typeahead) || len(got.Signals) != 0 || len(got.WriteErrors) != 0 {
		t.Fatalf("worker=%+v", got)
	}
	if !strings.HasPrefix(out.String(), "setup-started\n") || !strings.HasSuffix(out.String(), typeahead) || strings.Count(out.String(), "third") != 1 {
		t.Fatal("typeahead lost its order", len(out.String()))
	}
	r, err := h.s.Receipts.Read(req.ID)
	if err != nil || r.State != "complete" || r.Status != 17 || !r.SetupComplete || r.ClientDetached || h.s.RecoveryRequired {
		t.Fatal(r, err)
	}
	_ = local.Close()
	assertNoDescriptorLeak(t, before)
}

// A4: disconnecting after handoff ends the stream without fencing; an
// ordinary setup failure after client loss is a known nonzero result.
func TestOwnedAttachSettledOutcomesNeverFence(t *testing.T) {
	t.Run("disconnect after release", func(t *testing.T) {
		h := newOwnedHarness(t, Principal{})
		req := h.request("owned", false)
		conn, err := net.Dial("unix", h.socket)
		if err != nil {
			t.Fatal(err)
		}
		local, remote, err := termio.ViewPair()
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = local.Close() }()
		go func() { _, _ = io.Copy(io.Discard, local) }()
		input, keepOpen := io.Pipe()
		defer func() { _ = keepOpen.Close() }()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		echoed := make(chan struct{})
		var once sync.Once
		go func() {
			_ = Call(ctx, conn, hello(), req, ClientIO{In: input, Err: io.Discard, View: remote, Out: checkpointWriter(func(p []byte) (int, error) {
				if bytes.Contains(p, []byte("streaming")) {
					once.Do(func() { close(echoed) })
				}
				return len(p), nil
			})})
		}()
		h.await("arrived")
		for _, name := range []string{"start", "handoff"} {
			_ = os.WriteFile(filepath.Join(h.dir, name), nil, 0o600)
		}
		_, _ = keepOpen.Write([]byte("streaming"))
		h.wait(echoed, "the attached stream")
		cancel()
		h.wait(h.served, "the abandoned stream to end")
		r, err := h.s.Receipts.Read(req.ID)
		if err != nil || r.State != "setup-complete" || h.s.RecoveryRequired || h.holders() != 1 {
			t.Fatal(r, h.s.RecoveryRequired, err)
		}
		h.assertNextMutation()
	})
	t.Run("setup failure after client loss", func(t *testing.T) {
		h := newOwnedHarness(t, Principal{})
		req := h.request("owned-fail", false)
		_, conn := h.rawOwnedClient(req)
		h.await("arrived")
		_ = conn.Close()
		h.releaseBarriers()
		r, err := h.s.Receipts.Read(req.ID)
		if err != nil || r.State != "complete" || r.Status != 5 || r.SetupComplete || !r.ClientDetached || h.s.RecoveryRequired {
			t.Fatal(r, h.s.RecoveryRequired, err)
		}
		if got := h.result(); len(got.WriteErrors) != 0 || len(got.Signals) != 0 {
			t.Fatalf("worker=%+v", got)
		}
		h.assertNextMutation()
	})
}

// A6: genuine controller-side failures keep today's recovery rules.
func TestOwnedAttachGenuineFailuresStillFence(t *testing.T) {
	t.Run("worker killed after effects", func(t *testing.T) {
		h := newOwnedHarness(t, Principal{})
		req := h.request("owned-kill", false)
		_, conn := h.rawOwnedClient(req)
		h.await("arrived")
		_ = conn.Close()
		h.releaseBarriers()
		r, err := h.s.Receipts.Read(req.ID)
		if err != nil || r.State != "uncertain" || !h.s.RecoveryRequired {
			t.Fatal(r, h.s.RecoveryRequired, err)
		}
	})
	t.Run("operator authority revoked", func(t *testing.T) {
		h := newOwnedHarness(t, Principal{Box: "caller", Incarnation: "live"})
		var revoked atomic.Bool
		h.s.CheckPrincipal = func(context.Context, Principal) error {
			if revoked.Load() {
				return errors.New("operator grant removed")
			}
			return nil
		}
		req := h.request("owned", false)
		_, conn := h.rawOwnedClient(req)
		h.await("arrived")
		_ = conn.Close()
		h.wait(h.detached, "the client's departure")
		revoked.Store(true)
		h.wait(h.served, "the revoked setup to end")
		r, err := h.s.Receipts.Read(req.ID)
		if err != nil || r.State != "uncertain" || !h.s.RecoveryRequired || h.holders() != 0 {
			t.Fatal(r, h.s.RecoveryRequired, err)
		}
	})
	t.Run("drain expiry", func(t *testing.T) {
		h := newOwnedHarness(t, Principal{})
		req := h.request("owned", false)
		_, conn := h.rawOwnedClient(req)
		h.await("arrived")
		_ = conn.Close()
		h.wait(h.detached, "the client's departure")
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		if err := h.s.Drain(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("drain did not wait for the owned setup", err)
		}
		if r, err := h.s.Receipts.Read(req.ID); err != nil || r.State != "pending" {
			t.Fatal("drain expiry touched the owned setup", r, err)
		}
		h.releaseBarriers()
		h.assertOwnedSetupFinished(req)
		if err := h.s.Drain(context.Background()); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("controller restart mid-setup", func(t *testing.T) {
		receipts := Receipts{t.TempDir()}
		r, err := receipts.begin(request("box.attach"), Principal{}, "generation-1", "", true)
		if err != nil {
			t.Fatal(err)
		}
		r.EffectPhase = EffectStarted
		r.ClientDetached = true
		if err := receipts.Save(r); err != nil {
			t.Fatal(err)
		}
		if interrupted, err := receipts.Recover(); err != nil || !interrupted {
			t.Fatal("restart lost an owned setup's uncertainty", interrupted, err)
		}
	})
}

// assertOwnedSetupFinished checks a drained owned setup completed on its own.
func (h *ownedHarness) assertOwnedSetupFinished(req Request) {
	h.t.Helper()
	r, err := h.s.Receipts.Read(req.ID)
	if err != nil || r.State != "complete" || !r.SetupComplete || h.s.RecoveryRequired || h.holders() != 1 {
		h.t.Fatal(r, h.s.RecoveryRequired, err)
	}
}

func TestRetainedOutputRingKeepsTheNewestBytes(t *testing.T) {
	var out clientOutput
	out.detach()
	sink := clientSink{checkpointWriter(func([]byte) (int, error) { return 0, errors.New("unused") }), &out}
	payload := bytes.Repeat([]byte("abcdefgh"), retainedOutputLimit/8+3)
	if n, err := sink.Write(payload); n != len(payload) || err != nil {
		t.Fatal(n, err)
	}
	ordered := append(append([]byte{}, out.retained[out.start:]...), out.retained[:out.start]...)
	if len(out.retained) != retainedOutputLimit || out.accepted != int64(len(payload)) || out.dropped != 24 || !bytes.Equal(ordered, payload[24:]) {
		t.Fatal("retained output", len(out.retained), out.accepted, out.dropped)
	}
}

func TestClientSinkDetachesOnceAndNeverFails(t *testing.T) {
	detaches := 0
	out := &clientOutput{onDetach: func() { detaches++ }}
	failed := false
	sink := clientSink{checkpointWriter(func(p []byte) (int, error) {
		if failed {
			t.Fatal("wrote to a detached client")
		}
		failed = true
		return 2, errors.New("client gone")
	}), out}
	for range 3 {
		if n, err := sink.Write([]byte("payload")); n != 7 || err != nil {
			t.Fatal(n, err)
		}
	}
	if detaches != 1 || out.accepted != 21 || string(out.retained) != "yloadpayloadpayload" {
		t.Fatal(detaches, out.accepted, string(out.retained))
	}
}

func openDescriptors(t *testing.T) int {
	t.Helper()
	dir := "/proc/self/fd"
	if runtime.GOOS != "linux" {
		dir = "/dev/fd" // macOS lists this process's descriptors here
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

func assertNoDescriptorLeak(t *testing.T, before int) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if openDescriptors(t) <= before {
			return
		}
	}
	t.Fatalf("descriptors leaked: %d before, %d after", before, openDescriptors(t))
}
