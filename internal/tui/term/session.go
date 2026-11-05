package term

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/leogr/yoloctl/internal/session"
	"github.com/leogr/yoloctl/internal/terminal"
	"github.com/leogr/yoloctl/internal/termio"
)

// Options configures one tab session.
type Options struct {
	// TrustedView is only for the yoloctl CLI child, never arbitrary programs.
	TrustedView bool
	Argv        []string // command and args; runs under a fresh PTY
	Env         []string // base env; nil means os.Environ()

	Rows, Cols             int
	CellWidth, CellHeight  int
	ScrollbackLines        int // 0 → DefaultScrollbackLines
	Foreground, Background color.Color
	Palette                [256]color.Color

	// OnData wakes the renderer after each PTY read batch. Called from
	// I/O workers and on input rejection; must not block or reenter the terminal.
	OnData func()

	// OnOSC52 receives agent-to-host clipboard controls before the emulator
	// consumes them. Called serially from the pump goroutine.
	OnOSC52 func(termio.OSC52)
	// OnBell delivers a batch of native bell events from the output pump.
	OnBell func(int)
	// OnBrowser is installed only by the outer host frontend. It must queue
	// a model event; desktop I/O must not run on the session reader.
	OnBrowser func(*Session, session.BrowserRequest, <-chan struct{})
	// OnTabRequest gives a trusted child a private channel to ask for new
	// attach tabs; only `space view` tabs get one. It is called from a reader
	// goroutine and must only queue a model event.
	OnTabRequest func(termio.TabRequest)
}

// Session is one PTY child rendered through the emulator: the unit a
// TUI tab embeds. Commands run via the yoloctl binary as subprocesses.
type Session struct {
	*emulator
	ptmx      *os.File
	cmd       *exec.Cmd
	writer    *inputQueue
	managed   *managedView
	onBrowser func(*Session, session.BrowserRequest, <-chan struct{})

	done    chan struct{} // closed when pump finishes (status written)
	exited  chan struct{} // closed when the direct child has been reaped
	waitErr error         // set before exited closes; read after
	release func(time.Time)
	// drainBy bounds the whole post-reap drain (slave release plus reader
	// join); set before exited closes.
	drainBy time.Time
}

// Start launches argv under a PTY of the given size and begins pumping
// its output through the emulator. The child's TERM is always
// xterm-256color, independently of the host's TERM. This is an advertised
// contract, not proof that all of its capabilities are implemented.
// YOLOCTL_TUI=1 marks the
// nesting (insurance only; recursion is structurally impossible).
func Start(opts Options) (*Session, error) {
	if len(opts.Argv) == 0 {
		return nil, fmt.Errorf("term: session needs an argv")
	}
	if opts.Rows <= 0 || opts.Cols <= 0 {
		return nil, fmt.Errorf("term: session needs a positive PTY size")
	}
	cmd := exec.Command(opts.Argv[0], opts.Argv[1:]...)
	env := opts.Env
	if env == nil {
		env = os.Environ()
	}
	cmd.Env = forceEnv(env, "TERM=xterm-256color", "YOLOCTL_TUI=1")
	emu := newEmulator(opts.Rows, opts.Cols, nil, opts.ScrollbackLines)
	if emu.err != nil {
		return nil, emu.err
	}
	emu.onFrame = opts.OnData
	size := terminal.Geometry{Rows: opts.Rows, Cols: opts.Cols, CellWidth: opts.CellWidth, CellHeight: opts.CellHeight}
	if err := emu.ResizeGeometry(size); err != nil {
		_ = emu.Close()
		return nil, err
	}

	var viewParent, viewChild *os.File
	if opts.TrustedView {
		var err error
		viewParent, viewChild, err = termio.ViewPair()
		if err != nil {
			_ = emu.Close()
			return nil, err
		}
		cmd.ExtraFiles = []*os.File{viewChild}
		cmd.Env = forceEnv(cmd.Env, termio.ViewFDEnv+"=3")
	}
	var requestParent, requestChild *os.File
	if opts.OnTabRequest != nil {
		var err error
		requestParent, requestChild, err = termio.TabRequestPair()
		if err != nil {
			if viewParent != nil {
				_ = viewParent.Close()
				_ = viewChild.Close()
			}
			_ = emu.Close()
			return nil, err
		}
		cmd.ExtraFiles = append(cmd.ExtraFiles, requestChild)
		cmd.Env = forceEnv(cmd.Env, fmt.Sprintf("%s=%d", termio.TabRequestFDEnv, 2+len(cmd.ExtraFiles)))
	}
	ptmx, releaseSlave, err := startPTYGeometry(cmd, size)
	if viewChild != nil {
		_ = viewChild.Close()
	}
	if requestChild != nil {
		_ = requestChild.Close()
	}
	if err != nil {
		if viewParent != nil {
			_ = viewParent.Close()
		}
		if requestParent != nil {
			_ = requestParent.Close()
		}
		_ = emu.Close()
		return nil, fmt.Errorf("term: start %s: %w", opts.Argv[0], err)
	}
	if requestParent != nil {
		go func() {
			defer func() { _ = requestParent.Close() }()
			_ = termio.ReadTabRequests(requestParent, opts.OnTabRequest)
		}()
	}
	writer := newInputQueue(ptmx, opts.OnData)
	emu.input.setTarget(writer)
	s := &Session{
		// Replies and user input enter the same ordered PTY queue.
		emulator:  emu,
		ptmx:      ptmx,
		cmd:       cmd,
		writer:    writer,
		onBrowser: opts.OnBrowser,
		release:   releaseSlave,
		done:      make(chan struct{}),
		exited:    make(chan struct{}),
	}
	s.SetOSC52Handler(opts.OnOSC52)
	s.SetBellHandler(opts.OnBell)
	s.SetColors(opts.Foreground, opts.Background)
	s.SetPalette(opts.Palette)
	if viewParent != nil {
		s.startView(viewParent)
	}
	go s.waitChild()
	go s.pump(opts.OnData)
	return s, nil
}

// waitChild reaps the direct child exactly once (the sole cmd.Wait
// caller), records its status, and signals exited. Close / Wait only
// signal the child (Process.Kill) and never call cmd.Wait themselves, so
// there's no double-reap.
func (s *Session) waitChild() {
	s.waitErr = s.cmd.Wait()
	s.drainBy = time.Now().Add(ptyDrainGrace)
	s.release(s.drainBy)
	close(s.exited)
}

// pump copies PTY output into the emulator. Completion is gated on the
// DIRECT child exiting (the exited signal), NOT on PTY EOF: a command
// that daemonizes a grandchild holding the slave open — e.g. an
// ensureSpace that runs `limactl start`, whose long-lived host-agent
// inherits this PTY as a controlling terminal — would otherwise never EOF,
// leaving the tab "running" forever.
func (s *Session) pump(onData func()) {
	readDone := make(chan struct{})
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := s.ptmx.Read(buf)
			if n > 0 {
				_, _ = s.writeOutput(buf[:n], true) // atomic with publishing a snapshot
				if onData != nil {
					onData()
				}
			}
			if err != nil {
				break
			}
		}
		s.FlushOutputFilters()
		close(readDone)
	}()

	<-s.exited
	// Child reaped and the parent's slave released. The reader gets the rest
	// of the same post-reap budget to flush the final output (only matters
	// when a grandchild still holds the slave; otherwise the master EOFs).
	select {
	case <-readDone:
	case <-time.After(time.Until(s.drainBy)):
	}
	// The pollable master makes closing interrupt outstanding I/O. Join both
	// pumps before publishing completion so neither input nor output survives
	// into a completed tab. Descendants may still retain the slave.
	s.input.setTarget(nil)
	s.writer.Close()
	<-readDone

	if s.managed != nil {
		select {
		case <-s.managed.done:
		case <-time.After(5 * time.Second):
			s.managed.cancel()
			<-s.managed.done
		}
		s.managed.cancel()
		s.finishView()
	}
	writeExitBanner(s.emulator, exitCode(s.cmd, s.waitErr))
	s.EndRenderHold()
	if onData != nil {
		onData()
	}
	close(s.done)
}

// writeExitBanner appends the exit banner once the child is reaped: the
// same exit status line the palette console shows — green ✓ on success,
// red ✗ on failure — plus a dim "press Enter to close" affordance, so the
// tab reports how its command ended and how to dismiss it (the TUI closes
// the tab on Enter). A child killed mid-frame (box/VM stop under a
// full-screen agent) leaves a scroll region set and the cursor parked
// inside its dead UI — printing there splices the banner into leftover
// frame rows. SessionEndResetInPlace first normalizes and re-parks on the
// bottom row of whichever screen the child died on (never switching off the
// alt screen). The banner then relocates to the lowest visible content row
// before appending. That preserves short command output when unused rows are
// available while still scrolling a full dead frame enough to make room.
// Exactly one blank line separates the previous visible content and banner.
func writeExitBanner(e *emulator, code int) {
	var status string
	if code == 0 {
		status = "\x1b[32m✓ exit 0\x1b[0m"
	} else {
		status = fmt.Sprintf("\x1b[31m✗ exit %d\x1b[0m", code)
	}
	_, _ = e.Write([]byte(termio.SessionEndResetInPlace))
	row, hasContent := e.lastContentRow()
	sep := "\r\n"
	if hasContent {
		sep += "\r\n"
	}
	_, _ = e.Write([]byte(fmt.Sprintf("\x1b[%d;1H", row+1) + sep + "\x1b[K" + status +
		"\r\n\x1b[K\x1b[38;5;243mPress Enter to close this tab\x1b[0m\r\n"))
}

// SendInput shares one ordered queue with terminal query replies.
func (s *Session) SendInput(p []byte) error {
	_, err := s.input.Write(p)
	return err
}

func (s *Session) PrepareInput(prepare func(context.Context) ([]byte, error)) error {
	return s.writer.Transform(prepare)
}

func (s *Session) Host() *session.HostClient {
	if s.managed == nil {
		return nil
	}
	return s.managed.host
}

// TakeInputError consumes a pending input-queue error for the UI status line.
func (s *Session) TakeInputError() error {
	return s.writer.takeError()
}

// TakeViewError consumes a fatal managed-attachment diagnostic. Keep it
// separate from transient input notices so the TUI can show a command result
// in its palette, or defer presentation while a palette command is running.
func (s *Session) TakeViewError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.viewError
	s.viewError = nil
	return err
}

// Resize propagates a new geometry to both the PTY (SIGWINCH to the
// child) and the emulator grid.
func (s *Session) Resize(rows, cols int) error {
	g := s.LocalGeometry()
	g.Rows, g.Cols = max(rows, 1), max(cols, 1)
	return s.ResizeGeometry(g)
}

func (s *Session) ResizeGeometry(g terminal.Geometry) error {
	// Remember this viewer's geometry even while the live replica waits for
	// canonical resize events. Completed tabs resize locally without a PTY.
	if err := s.emulator.ResizeGeometry(g); err != nil {
		return err
	}
	select {
	case <-s.done:
		return nil
	default:
	}
	if s.managed != nil {
		select {
		case <-s.managed.sizes:
		default:
		}
		select {
		case s.managed.sizes <- g:
		default:
		}
		if s.managed.active.Load() {
			return nil
		}
	}
	err := resizePTYGeometry(s.ptmx, g)
	// The pump closes the PTY only after the child exits, before Done: a
	// resize in that window is local, as for a completed tab.
	select {
	case <-s.exited:
		return nil
	default:
	}
	return err
}

// Done closes when the PTY stream has ended (child exited or killed).
func (s *Session) Done() <-chan struct{} { return s.done }

// HolderView reports whether the session is a managed view whose holder
// handshake is established: its agent session outlives this client.
func (s *Session) HolderView() bool { return s.managed != nil && s.managed.active.Load() }

// Wait blocks until the pump finishes (child exited + status written) or
// timeout elapses — on timeout the child is killed, which makes the pump
// finish. Returns the child's exit error.
func (s *Session) Wait(timeout time.Duration) error {
	select {
	case <-s.done:
	case <-time.After(timeout):
		if s.cmd.Process != nil {
			_ = s.cmd.Process.Kill()
		}
		<-s.done
	}
	if s.managed != nil {
		return errors.Join(s.waitErr, s.managed.err)
	}
	return s.waitErr
}

// Close force-terminates the child and waits for the pump to finish. Tab
// close in the TUI detaches (the child is `yoloctl box attach`, and
// the agent outlives it under the session model) — killing the client
// subprocess is exactly a detach, never an agent stop. The kill makes
// waitChild's cmd.Wait return, which unblocks the pump.
func (s *Session) Close() {
	if s.managed != nil {
		s.managed.cancel()
	}
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	<-s.done
	_ = s.emulator.Close()
}

// forceEnv returns base with each KEY=VALUE in forced applied,
// replacing any existing KEY entry.
func forceEnv(base []string, forced ...string) []string {
	out := make([]string, 0, len(base)+len(forced))
	for _, kv := range base {
		drop := false
		for _, f := range forced {
			if eq := strings.IndexByte(f, '='); eq > 0 && strings.HasPrefix(kv, f[:eq+1]) {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, kv)
		}
	}
	return append(out, forced...)
}
