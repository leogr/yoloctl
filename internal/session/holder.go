package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/leogr/yoloctl/internal/terminal"
	"github.com/leogr/yoloctl/internal/termio"
	"golang.org/x/crypto/ssh"
)

// HolderOptions configures one session holder run. Zero values take the
// package defaults where one exists.
type HolderOptions struct {
	Agentbox      string
	SSHSocket     string // sandbox SSH unix socket (sandbox.SocketHostPath)
	AttachSocket  string // attach socket to serve (SocketPath)
	MarkerPath    string
	TombstonePath string
	// LaunchToken is copied into the marker and the tombstone (see
	// Marker.LaunchToken). Empty writes neither.
	LaunchToken string

	Argv []string // agent command and args, run inside the sandbox

	Term       string // TERM for the agent's PTY (default xterm-256color)
	Cols, Rows int    // initial PTY size; the first client RESIZE corrects it

	RingBytes         int
	KeepaliveInterval time.Duration

	// TitleTemplate seeds the initial title and composes agent-set OSC 0/1/2 titles: an
	// EXPANDED tui.yaml box_title_template (ExpandBoxTitleTemplate's
	// output — literal text plus at most one {title}), forwarded by
	// StartHolderUnit. Empty keeps the default "<agentbox>: <title>".
	TitleTemplate string
	// TitleStrip is removed, with the spaces after it, from the front of
	// each agent-set title: the agent icon when the template shows
	// {agent-icon}. Empty strips nothing.
	TitleStrip string
	// HostIntegration installs a session-scoped confined browser helper and
	// accepts image uploads. Host clients retain all desktop authorization.
	HostIntegration bool
	// Canonical host workdir for optional image reuse; empty for ephemeral boxes.
	HostWorkdir string
	// Daemons are the agent profile's declared daemons (session.daemons),
	// stopped before the agent starts and after it exits (see ReapScript).
	Daemons []string

	Logf func(format string, args ...any) // optional progress/diagnostic log
}

// sshDialTimeout bounds the unix-socket dial plus SSH handshake to the
// sandbox.
const sshDialTimeout = 10 * time.Second

// ErrHolderConnLost reports that the SSH channel to the sandbox died
// without delivering an agent exit status (sandbox crash, VM trouble, or
// a dropped channel). The agent's state is unknown: the marker is left
// in place so the CLI treats the box as possibly-orphaned (spike F2).
var ErrHolderConnLost = errors.New("session: connection to sandbox lost without an exit status")

// RunHolder runs the session holder until the agent exits, the context
// is canceled, or the sandbox connection is lost.
//
// Returns (agent exit status, nil) when the agent exited through the
// holder — including nonzero statuses; the tombstone is written and the
// marker removed before returning. Returns (0, ctx.Err()) on context
// cancellation (unit stop): per spike F1 the agent is deliberately left
// running and the marker stays. Returns (-1, ErrHolderConnLost) when the
// channel died statusless: the marker stays (possible orphan).
func RunHolder(ctx context.Context, opts HolderOptions) (int, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if opts.Agentbox == "" || opts.SSHSocket == "" || opts.AttachSocket == "" ||
		opts.MarkerPath == "" || opts.TombstonePath == "" || len(opts.Argv) == 0 {
		return -1, errors.New("session: holder needs agentbox, sockets, marker, tombstone and argv")
	}
	// The persistent parser owns the advertised terminal identity.
	opts.Term = "xterm-256color"
	if opts.Cols <= 0 {
		opts.Cols = 80
	}
	if opts.Rows <= 0 {
		opts.Rows = 24
	}
	if opts.RingBytes <= 0 {
		opts.RingBytes = DefaultRingBytes
	}
	if opts.KeepaliveInterval <= 0 {
		opts.KeepaliveInterval = DefaultKeepaliveInterval
	}
	logf := opts.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	reaper, err := ReapScript(opts.Daemons)
	if err != nil {
		return -1, err
	}

	h := &holder{
		hostCtx:       ctx,
		ring:          NewRing(opts.RingBytes),
		terminalInput: &terminalInputState{},
		logf:          logf,
		input:         make(chan []byte, 128),
	}
	defer func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.cancelHoldLocked()
		h.terminalInput.Close()
		if h.vt != nil {
			_ = h.vt.Close()
		}
	}()
	logf("initializing terminal")
	if err := h.terminalInput.Init(); err != nil {
		return -1, fmt.Errorf("session: initialize input replay: %w", err)
	}

	if err := h.initView(opts.Rows, opts.Cols); err != nil {
		return -1, err
	}
	logf("terminal initialized; connecting sandbox")

	// SSH to the sandbox socket. The socket speaks plain SSH with
	// auth_none; access is gated by filesystem permissions (root-only on
	// the VM), which is also why ignoring the host key is sound here:
	// the endpoint's identity is the root-owned socket path itself.
	conn, err := net.DialTimeout("unix", opts.SSHSocket, sshDialTimeout)
	if err != nil {
		return -1, fmt.Errorf("session: dial sandbox socket: %w", err)
	}
	// NewClientConn does not apply ClientConfig.Timeout to an existing socket.
	// Bound the handshake itself and close it when the holder is canceled.
	_ = conn.SetDeadline(time.Now().Add(sshDialTimeout))
	stopHandshake := context.AfterFunc(ctx, func() { _ = conn.Close() })
	cc, chans, reqs, err := ssh.NewClientConn(conn, opts.Agentbox, &ssh.ClientConfig{
		User:            "sandbox",
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         sshDialTimeout,
	})
	stopHandshake()
	if err != nil {
		_ = conn.Close()
		return -1, fmt.Errorf("session: ssh handshake: %w", err)
	}
	_ = conn.SetDeadline(time.Time{})
	logf("sandbox connected; preparing session")
	client := ssh.NewClient(cc, chans, reqs)
	defer func() { _ = client.Close() }()
	command := shellJoin(opts.Argv)
	if opts.HostIntegration {
		opening := make(chan struct{}, 2)
		h.upload = func(ctx context.Context, data []byte) (string, error) { return uploadImage(ctx, client, opening, data) }
		h.reuse = func(ctx context.Context, probe imageProbe) (string, error) {
			return reuseImage(ctx, client, opening, opts.HostWorkdir, probe)
		}
		bridge, err := h.startBrowserBridge(client, opening)
		if err != nil {
			logf("browser integration unavailable: %v", err)
		} else {
			defer func() { _ = bridge.session.Close() }()
			command = browserLaunch(opts.Argv, bridge.dir)
		}
	}

	sess, err := client.NewSession()
	if err != nil {
		return -1, fmt.Errorf("session: open channel: %w", err)
	}
	defer func() { _ = sess.Close() }()
	if err := requestPTY(sess, opts.Term, terminal.Geometry{Rows: opts.Rows, Cols: opts.Cols}); err != nil {
		return -1, fmt.Errorf("session: request pty: %w", err)
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		return -1, fmt.Errorf("session: stdin pipe: %w", err)
	}
	// With a PTY the agent's stderr is merged into the PTY stream, so
	// stdout is the whole story.
	stdout, err := sess.StdoutPipe()
	if err != nil {
		return -1, fmt.Errorf("session: stdout pipe: %w", err)
	}
	h.sess = sess
	h.stdin = stdin
	inputCtx, inputCancel := context.WithCancel(ctx)
	defer inputCancel()
	go func() {
		for {
			select {
			case <-inputCtx.Done():
				return
			case p := <-h.input:
				if _, err := stdin.Write(p); err != nil {
					return
				}
			}
		}
	}()

	// Marker before Start: if the holder dies in between, the worst case
	// is a false "possible orphan" hint (cleared by recreate). The other
	// order risks an agent running with no marker — an undetected orphan
	// that a later run/attach could double-spawn against (spike F2).
	if err := WriteMarker(opts.MarkerPath, Marker{StartedAt: time.Now(), HolderPID: os.Getpid(), LaunchToken: opts.LaunchToken}); err != nil {
		return -1, fmt.Errorf("session: write marker: %w", err)
	}
	if err := sess.Start(reapBefore(reaper, command)); err != nil {
		_ = RemoveMarker(opts.MarkerPath) // no agent was started
		return -1, fmt.Errorf("session: start agent: %w", err)
	}
	logf("agent started: %s", strings.Join(opts.Argv, " "))

	tr := newTitleRewriter(opts.Agentbox)
	if opts.TitleTemplate != "" {
		tr = newTemplateTitleRewriter(opts.TitleTemplate, opts.TitleStrip)
	}
	// Publish identity before draining PTY output or admitting clients. Agents
	// that never set a title still identify their box; an immediate application
	// title wins, and quiet startup snapshots/compatibility replay have a title.
	if initial := tr.InitialTitle(); len(initial) > 0 {
		h.broadcast(initial)
	}

	// Start draining the PTY before serving attach clients so detached
	// startup output lands in the replay ring even when the agent speaks
	// immediately after exec.
	outputDone := make(chan struct{})
	go func() {
		defer close(outputDone)
		buf := make([]byte, maxPayload)
		for {
			n, rerr := stdout.Read(buf)
			if n > 0 {
				h.ptyOutput.Add(uint64(n))
				h.broadcast(tr.Rewrite(buf[:n]))
			}
			if rerr != nil {
				break
			}
		}
		if tail := tr.Flush(); len(tail) > 0 {
			h.broadcast(tail)
		}
	}()

	if err := os.MkdirAll(filepath.Dir(opts.AttachSocket), 0o700); err != nil {
		return -1, fmt.Errorf("session: socket dir: %w", err)
	}
	_ = os.Remove(opts.AttachSocket) // stale socket from a dead holder
	ln, err := net.Listen("unix", opts.AttachSocket)
	if err != nil {
		return -1, fmt.Errorf("session: listen attach socket: %w", err)
	}
	defer func() {
		_ = ln.Close()
		_ = os.Remove(opts.AttachSocket)
	}()
	acceptDone := make(chan struct{})
	go func() { defer close(acceptDone); h.acceptLoop(ln) }()

	// Keepalive (spike F3): any client→server packet resets russh's
	// inactivity timer; a global request is the cheapest one.
	kaCtx, kaCancel := context.WithCancel(ctx)
	defer kaCancel()
	go func() {
		t := time.NewTicker(opts.KeepaliveInterval)
		defer t.Stop()
		for {
			select {
			case <-kaCtx.Done():
				return
			case <-t.C:
				if _, _, err := client.SendRequest("keepalive@yoloctl.dev", true, nil); err != nil {
					logf("keepalive failed: %v", err)
					return
				}
			}
		}
	}()

	// Context cancellation (systemd stop → SIGTERM → main's signal
	// context). Per spike F1 we do NOT signal the agent (no-op upstream)
	// and do not want to: holder stop must leave the agent running.
	// Closing the connection unblocks the pump below.
	stopping := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			close(stopping)
			logf("stopping (context canceled); agent stays running")
			_ = client.Close()
		case <-h.failed:
			h.mu.Lock()
			err := h.viewErr
			h.mu.Unlock()
			logf("authoritative terminal failed: %v", err)
			_ = client.Close()
		case <-h.done():
		}
	}()

	<-outputDone
	// Stop admission before draining writers. No client can register after
	// closeClients takes its final inventory and before the holder exits.
	_ = ln.Close()
	<-acceptDone
	h.admissions.Wait()

	select {
	case <-stopping:
		// Deliberate stop: agent left running, marker left in place.
		h.closeClients(nil)
		return 0, ctx.Err()
	default:
	}

	werr := sess.Wait()
	status := 0
	var connLost bool
	if werr != nil {
		var ee *ssh.ExitError
		var em *ssh.ExitMissingError
		switch {
		case errors.As(werr, &ee):
			status = ee.ExitStatus()
		case errors.As(werr, &em):
			// Channel closed without an exit-status message: statusless.
			connLost = true
		default:
			connLost = true
		}
	}
	h.markDone()

	if connLost {
		// Agent state unknown — keep the marker so the CLI can flag a
		// possible orphan; no tombstone (there is no status to record).
		logf("sandbox connection lost: %v", werr)
		h.closeClients(nil)
		return -1, fmt.Errorf("%w: %v", ErrHolderConnLost, werr)
	}

	logf("agent exited with status %d", status)
	// Before the tombstone: the next launch must not meet this launch's
	// daemons. A holder stop never gets here, so a deliberately detached
	// agent keeps its daemons.
	reapAfterExit(ctx, client, reaper, logf)
	if err := WriteTombstone(opts.TombstonePath, Tombstone{ExitCode: status, ExitedAt: time.Now(), LaunchToken: opts.LaunchToken}); err != nil {
		logf("write tombstone: %v", err)
	}
	if err := RemoveMarker(opts.MarkerPath); err != nil {
		logf("remove marker: %v", err)
	}
	h.closeClients(encodeExit(status))
	return status, nil
}

// holder is the shared state between the output pump, the accept loop,
// and per-client read loops.
type holder struct {
	mu                  sync.Mutex
	resizeMu            sync.Mutex
	failed              chan struct{}
	pendingReplies      []byte
	holdTimer           *time.Timer
	holdEpoch           uint64
	ring                *Ring
	ringFilter          termio.OSC52Scanner
	terminalInput       *terminalInputState
	clients             []*holderClient
	closed              bool   // closeClients ran: acceptLoop must not register anyone
	exitPayload         []byte // what closeClients sent, owed to clients accepted later
	sess                *ssh.Session
	stdin               io.WriteCloser
	doneCh              chan struct{}
	once                sync.Once
	logf                func(string, ...any)
	vt                  *terminal.Terminal
	colors              *terminal.ColorQueries
	rawRingFilter       termio.OSC52Scanner
	sequence            uint64
	bundle, incarnation string
	viewErr             error
	input               chan []byte
	admissions          sync.WaitGroup
	hostCtx             context.Context
	browserPending      map[string]browserPending
	activity            uint64
	lastInput           InputActivity
	imageBytes          int
	upload              func(context.Context, []byte) (string, error)
	reuse               func(context.Context, imageProbe) (string, error)
	// Workload counters for read-only stats: bytes read from the agent's PTY
	// and accepted participant input bytes (activity counts the packets).
	// Synthetic titles, replay, snapshots, fan-out and query replies are not
	// workload bytes. ptyOutput is atomic so the output pump never waits.
	ptyOutput  atomic.Uint64
	inputBytes uint64
}

func (h *holder) done() chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.doneCh == nil {
		h.doneCh = make(chan struct{})
	}
	return h.doneCh
}

func (h *holder) markDone() {
	ch := h.done()
	h.once.Do(func() { close(ch) })
}

// broadcast appends p to the ring and queues DATA for each client without
// waiting for socket I/O. A full queue disconnects only that slow client.
func (h *holder) broadcast(p []byte) {
	h.mu.Lock()
	var dropped []*holderClient
	// Advance the authoritative parser even with no viewers. Queries go back
	// exactly once via its bounded input queue. Clipboard effects are separate
	// live events and never enter the native snapshot or raw replay ring.
	filtered, effects := h.ringFilter.Feed(p)
	if h.vt != nil && h.viewErr == nil {
		h.colors.Observe(filtered)
		if _, err := h.vt.Write(filtered); err != nil {
			h.failViewLocked(err)
		}
		h.flushRepliesLocked()
		dropped = append(dropped, h.emitLocked(frameData, filtered)...)
		for _, effect := range effects {
			payload, _ := json.Marshal(effect)
			if len(payload) > maxClipboardEvent {
				payload, _ = json.Marshal(termio.OSC52{Malformed: true})
			}
			dropped = append(dropped, h.emitLocked(frameClipboard, payload)...)
			dropped = append(dropped, h.emitLocked(frameClipboardEnd, []byte{0})...)
		}
	}
	raw, err := h.vt.FilterPassthrough(p)
	if err != nil {
		h.failViewLocked(err)
	}
	// The compatibility transcript has no queries, clipboard effects or bells.
	h.retainReplay(raw)
	p = bytes.Clone(raw)

	for i := 0; i < len(h.clients); i++ {
		c := h.clients[i]
		if !c.managed && !c.enqueue(p) {
			dropped = append(dropped, c)
			h.removeClientLocked(i)
			i--
		}
	}
	h.mu.Unlock()
	for _, c := range dropped {
		h.logf("client dropped (output queue full or closed)")
		c.abort()
	}
}

// replay requires h.mu. Advancing an unexportable continuation also advances
// the ring boundary, so its bytes are never applied twice by future attaches.
func (h *holder) retainReplay(p []byte) {
	ringBytes, _ := h.rawRingFilter.Feed(p)
	h.terminalInput.Feed(h.ring.Write(h.terminalInput.FilterBells(ringBytes)))
}

func (h *holder) replay() ([]byte, error) {
	prelude, err := h.terminalInput.Replay()
	if errors.Is(err, terminal.ErrReplayContinuation) {
		n, advanceErr := h.terminalInput.Recover(h.ring.Snapshot())
		h.ring.Discard(n)
		if advanceErr != nil {
			return nil, advanceErr
		}
		prelude, err = h.terminalInput.Replay()
	}
	if err != nil {
		return nil, err
	}
	prelude = append(prelude, h.ring.Snapshot()...)
	// Withheld OSC/UTF-8 bytes precede their live continuation. A clipboard
	// sequence completed after this attach is a live effect, not a replay.
	return append(prelude, h.rawRingFilter.Pending()...), nil
}

// acceptLoop admits each client independently: handshake, snapshot or
// compatibility replay, then ordered live events until the connection drops.
func (h *holder) acceptLoop(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		h.admissions.Add(1)
		go func() { defer h.admissions.Done(); h.admitView(conn) }()
	}
}

func (h *holder) clientWriteLoop(c *holderClient, snapshot []byte) {
	defer close(c.done)
	if err := c.writeLoop(snapshot); err != nil {
		h.logf("client output closed: %v", err)
	}
	h.logf("client detached")
	h.mu.Lock()
	for i, cl := range h.clients {
		if cl == c {
			h.removeClientLocked(i)
			break
		}
	}
	dropped, _ := h.selectColorsLocked()
	h.mu.Unlock()
	h.abortDropped(dropped)
}

func (h *holder) removeClientLocked(i int) {
	copy(h.clients[i:], h.clients[i+1:])
	h.clients[len(h.clients)-1] = nil
	h.clients = h.clients[:len(h.clients)-1]
}

// clientReadLoop relays one client's frames: DATA to the agent's stdin,
// RESIZE to the PTY (mirror semantics: any client's resize applies).
func (h *holder) clientReadLoop(c *holderClient) {
	defer c.abort()
	inputOpen := true
	var images imageReceiver
	for {
		f, err := readFrame(c.conn)
		if err != nil {
			return
		}
		switch f.Type {
		case frameData:
			if inputOpen {
				h.mu.Lock()
				accepted := h.queueInput(f.Payload)
				var dropped []*holderClient
				if accepted && len(f.Payload) > 0 {
					h.activity++
					h.inputBytes += uint64(len(f.Payload))
					c.activity = h.activity
					c.accepted += uint64(len(f.Payload))
					h.lastInput = InputActivity{Sequence: h.activity, Source: c.id, Bytes: c.accepted}
					p, _ := json.Marshal(h.lastInput)
					dropped = h.emitLocked(frameActivity, p)
				}
				h.mu.Unlock()
				h.abortDropped(dropped)
				if !accepted {
					// The child may have closed stdin before its final output
					// and EXIT drain. Keep reading this attach until its socket
					// closes; failing input must not abort the output writer.
					inputOpen = false
					c.enqueueFrame(frameError, []byte("session input buffer full; input was not accepted; reattach before retrying"))
					c.finish(nil, time.Now().Add(clientWriteTimeout))
				}
			}
		case frameResize:
			size, err := decodeResize(f.Payload)
			if err != nil {
				h.logf("bad resize frame: %v", err)
				return
			}
			if err := h.resizeView(size); err != nil {
				return
			}
		case frameColors:
			if !c.managed || h.updateColors(c, f.Payload) != nil {
				return
			}
		case frameBrowserResult:
			if !c.managed || h.browserResult(c, f.Payload) != nil {
				return
			}
		case frameImageStart, frameImageChunk, frameImageEnd, frameImageProbe:
			if images.receive(h, c, f) != nil {
				return
			}
		default:
			// Unknown frame from a newer client: skip — the length
			// field already consumed the payload.
		}
	}
}

// closeClients optionally sends a final EXIT payload to every client,
// then closes them all. It ends the session for good: acceptLoop gives a
// client it picks up afterwards the same ending instead of registering it.
func (h *holder) closeClients(exitPayload []byte) {
	h.mu.Lock()
	h.closed = true
	h.cancelHoldLocked()
	h.exitPayload = exitPayload
	clients := h.clients
	h.clients = nil
	h.mu.Unlock()
	deadline := time.Now().Add(clientWriteTimeout)
	for _, c := range clients {
		c.finish(exitPayload, deadline)
	}
	for _, c := range clients {
		<-c.done
	}
}

// shellJoin renders argv as a single-quoted shell command line — the
// form openshell-sandbox's exec request expects (it runs the string
// through a shell). Same quoting as the CLI's shellQuote.
func shellJoin(argv []string) string {
	quoted := make([]string, len(argv))
	for i, a := range argv {
		quoted[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(quoted, " ")
}
