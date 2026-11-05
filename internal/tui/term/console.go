package term

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/terminal"
)

// ptyDrainGrace is the one post-reap budget: how long the pump waits, AFTER
// the direct child has exited, for the parent's slave release and the reader
// to flush the command's final output, before it force-closes the PTY
// master. The slave release can use part of it while queued output is still
// being read; it fully elapses only when a daemonized grandchild (e.g.
// `limactl start`'s long-lived host-agent) still holds the slave open — in
// the common case the master EOFs well within it.
const ptyDrainGrace = 200 * time.Millisecond

// Console is the TUI's persistent index-0 view (eyeball finding E5):
// one long-lived emulator that one-shot commands (`space ls`,
// `port list`, ...) append into shell-style, instead of each spawning
// a dead tab. Commands still run under a PTY (colors intact) and
// sequentially — the console is a log, not a multiplexer.
type Console struct {
	*emulator
	onData func()

	mu         sync.Mutex
	cmd        *exec.Cmd // running command, nil when idle
	writer     *inputQueue
	ptmx       *os.File
	done       chan struct{}
	closed     bool
	rows, cols int
}

// NewConsole builds the console emulator at the given size.
func NewConsole(rows, cols int, scrollbackLines int, onData func()) *Console {
	emu := newEmulator(rows, cols, nil, scrollbackLines)
	emu.trimFrame, emu.onFrame = true, onData
	return &Console{
		emulator: emu,
		onData:   onData,
		rows:     rows, cols: cols,
	}
}

// Run starts argv in the console, echoing the caller-styled display
// line first (the TUI renders a shell-like `❯ yoloctl <command>`
// prompt). The buffer is cleared: the palette overlay shows one
// command's output at a time ("ephemeral" — the next command replaces
// the previous). Returns false (and does nothing) while a previous
// command is still running — the caller surfaces "busy".
func (c *Console) Run(env []string, display string, argv ...string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return false, errors.New("terminal console closed")
	}
	if len(argv) == 0 {
		return false, errors.New("terminal console needs an argv")
	}
	if c.cmd != nil {
		return false, nil
	}
	c.Clear() // the embedded emulator's Clear
	if display == "" {
		display = "$ " + shellLine(argv)
	}
	c.echo(display + "\r\n")
	cmd := exec.Command(argv[0], argv[1:]...)
	if env == nil {
		env = os.Environ()
	}
	cmd.Env = forceEnv(env, "TERM=xterm-256color", "YOLOCTL_TUI=1")
	ptmx, releaseSlave, err := startPTYGeometry(cmd, c.LocalGeometry())
	if err != nil {
		c.echo(fmt.Sprintf("\x1b[31m%v\x1b[0m\r\n", err))
		return true, err
	}
	c.cmd, c.ptmx = cmd, ptmx
	c.done = make(chan struct{})
	c.writer = newInputQueue(ptmx, c.onData)
	c.input.setTarget(c.writer)
	go c.pump(cmd, ptmx, releaseSlave, c.writer)
	return true, nil
}

// ShowError presents a local command/attachment failure through the same
// wrapping and scrollback as command output. Never overwrite a running command.
// Diagnostics are text, not an additional terminal-control channel.
func (c *Console) ShowError(message string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.cmd != nil {
		return false
	}
	message = strings.Map(func(r rune) rune {
		if r != '\n' && r != '\t' && unicode.IsControl(r) {
			return -1
		}
		return r
	}, ansi.Strip(message))
	c.Clear()
	c.echo("\x1b[31mError: " + strings.ReplaceAll(message, "\n", "\r\n") + "\x1b[0m\r\n")
	return true
}

func (c *Console) pump(cmd *exec.Cmd, ptmx *os.File, releaseSlave func(time.Time), writer *inputQueue) {
	defer close(c.done)
	// Reader: copy PTY output into the emulator until the master errors
	// (EOF or close). Runs concurrently with the child wait below so a
	// process that daemonizes a grandchild holding the slave open can't
	// block completion.
	readDone := make(chan struct{})
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				_, _ = c.Write(buf[:n])
				if c.onData != nil {
					c.onData()
				}
			}
			if err != nil {
				break
			}
		}
		c.FlushOutputFilters()
		close(readDone)
	}()

	// Completion is the DIRECT child exiting — NOT PTY EOF. `limactl start`
	// (and anything else that daemonizes) leaves a long-lived grandchild
	// holding the PTY slave as its controlling terminal (an open fd on
	// /dev/tty), so the master never EOFs; gating on EOF would leave the
	// overlay "running" forever. Once the child is reaped, give the reader a
	// beat to flush the child's final output. The parent's slave is released
	// first (see startPTYGeometry); both share one post-reap budget.
	err := cmd.Wait()
	drainBy := time.Now().Add(ptyDrainGrace)
	releaseSlave(drainBy)
	select {
	case <-readDone:
	case <-time.After(time.Until(drainBy)):
	}
	// Interrupt and join both pumps before making the console available for
	// another command. This also prevents trailing filter state or a late reader
	// from writing into the next command's buffer.
	c.input.setTarget(nil)
	writer.Close()
	<-readDone
	c.EndRenderHold()
	// Always close with a status line — green ✓ on success, red ✗ on
	// failure — so the overlay shows every command's exit code; a blank
	// line after it (laid in by the overlay's content-fit padding)
	// separates it from the tab strip.
	if code := exitCode(cmd, err); code == 0 {
		c.echo("\x1b[32m✓ exit 0\x1b[0m\r\n")
	} else {
		c.echo(fmt.Sprintf("\x1b[31m✗ exit %d\x1b[0m\r\n", code))
	}
	c.mu.Lock()
	c.cmd, c.ptmx = nil, nil
	c.mu.Unlock()

	if c.onData != nil {
		c.onData()
	}
}

// SendInput feeds bytes to the running command's stdin (keystrokes,
// confirmations, a Ctrl+C ETX byte); dropped when idle.
func (c *Console) SendInput(p []byte) error {
	c.mu.Lock()
	writer, busy := c.writer, c.cmd != nil
	c.mu.Unlock()
	if !busy {
		return nil
	}
	_, err := writer.Write(p)
	return err
}

// TakeInputError consumes the current command's pending input error.
func (c *Console) TakeInputError() error {
	c.mu.Lock()
	writer := c.writer
	c.mu.Unlock()
	if writer == nil {
		return nil
	}
	return writer.takeError()
}

// Kill force-terminates the running command's process group — the
// escalation behind a second Ctrl+C, for a command that ignores SIGINT.
// The child is a session/group leader (the PTY sets Setsid), so the
// negative-pid signal reaches it and any of its children. No-op when idle.
func (c *Console) Kill() {
	c.mu.Lock()
	cmd := c.cmd
	c.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}

// Close ends the local command, joins its I/O workers and releases the core.
// A closed console cannot start another command.
func (c *Console) Close() error {
	c.mu.Lock()
	c.closed = true
	cmd, done := c.cmd, c.done
	if cmd != nil && cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	c.mu.Unlock()
	if done != nil {
		<-done
	}
	return c.emulator.Close()
}

// Busy reports whether a command is still running.
func (c *Console) Busy() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cmd != nil
}

// Resize adjusts the console grid (and the running command's PTY).
func (c *Console) Resize(rows, cols int) {
	// A collapsed overlay still needs a valid backing terminal for commands
	// and queries; zero-sized grids panic when the next character arrives.
	g := c.LocalGeometry()
	g.Rows, g.Cols = max(rows, 1), max(cols, 1)
	c.ResizeGeometry(g)
}

func (c *Console) ResizeGeometry(g terminal.Geometry) {
	if !g.Valid() {
		return
	}
	c.mu.Lock()
	c.rows, c.cols = g.Rows, g.Cols
	ptmx := c.ptmx
	c.mu.Unlock()
	_ = c.emulator.ResizeGeometry(g)
	if ptmx != nil {
		_ = resizePTYGeometry(ptmx, g)
	}
}

// echo writes presentation bytes straight into the emulator (caller
// holds c.mu or runs from the pump tail).
func (c *Console) echo(s string) {
	_, _ = c.Write([]byte(s))
	if c.onData != nil {
		c.onData()
	}
}

func shellLine(argv []string) string {
	return strings.Join(argv, " ")
}

// exitCode resolves a finished command's status for the overlay's
// closing line: the conventional 128+signum when the process was killed
// by a signal (Ctrl+C → SIGINT → 130, force-kill → SIGKILL → 137), else
// the plain exit code; 1 when Wait failed for a non-exit reason (start
// errors are handled before pump runs).
func exitCode(cmd *exec.Cmd, err error) int {
	if cmd.ProcessState == nil {
		if err != nil {
			return 1
		}
		return 0
	}
	if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return cmd.ProcessState.ExitCode()
}
