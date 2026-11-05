package term

// DIAGNOSTIC ONLY (branch diag/macos-pty-timeline, never merge): a bounded
// per-command timeline of the PTY reader, reap and drain, so a failing
// macOS CI run shows which ordering lost the output.

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

type ptyTimeline struct {
	mu     sync.Mutex
	start  time.Time
	events []string
}

func (t *ptyTimeline) reset() {
	t.mu.Lock()
	t.start, t.events = time.Now(), nil
	t.mu.Unlock()
}

func (t *ptyTimeline) add(format string, args ...any) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.events) >= 200 {
		return
	}
	t.events = append(t.events, fmt.Sprintf("%8.3fms %s", float64(time.Since(t.start).Microseconds())/1000, fmt.Sprintf(format, args...)))
}

func (t *ptyTimeline) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.Join(t.events, "\n")
}

// pending reports FIONREAD on the master (bytes the kernel holds unread).
func pending(f *os.File) string {
	raw, err := f.SyscallConn()
	if err != nil {
		return "err=" + err.Error()
	}
	n, ioctlErr := 0, error(nil)
	if err := raw.Control(func(fd uintptr) { n, ioctlErr = unix.IoctlGetInt(int(fd), fionread) }); err != nil {
		return "err=" + err.Error()
	}
	if ioctlErr != nil {
		return "err=" + ioctlErr.Error()
	}
	return fmt.Sprint(n)
}

// PTYTimeline returns the session's diagnostic timeline.
func (s *Session) PTYTimeline() string { return s.timeline.String() }

// PTYTimeline returns the last console command's diagnostic timeline.
func (c *Console) PTYTimeline() string { return c.timeline.String() }

// diagGrid trims trailing blank rows and bounds the text for the timeline.
func diagGrid(grid string) string {
	grid = strings.TrimRight(grid, " \n")
	if len(grid) > 400 {
		grid = grid[:400] + "…"
	}
	return grid
}
