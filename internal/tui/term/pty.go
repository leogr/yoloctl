package term

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/creack/pty"
	"github.com/leogr/yoloctl/internal/terminal"
	"golang.org/x/sys/unix"
)

// startPTYGeometry registers the master with Go's poller so closing it interrupts both
// reads and writes, even when descendants retain the slave. creack/pty uses Fd
// during setup, which leaves its original os.File in blocking mode. Duplicate
// the descriptor and construct a fresh File from an explicitly nonblocking fd;
// this gives it an independent lifetime without retaining the original wrapper.
func startPTYGeometry(cmd *exec.Cmd, g terminal.Geometry) (*os.File, error) {
	if !g.Valid() {
		return nil, errors.New("invalid PTY geometry")
	}
	x, y := ptyPixels(g)
	master, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: uint16(g.Rows), Cols: uint16(g.Cols), X: x, Y: y})
	if err != nil {
		return nil, err
	}
	pollable, err := pollablePTY(master)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("prepare interruptible PTY: %w", err)
	}
	return pollable, nil
}

// pollablePTY consumes master on both success and error.
func pollablePTY(master *os.File) (*os.File, error) {
	raw, err := master.SyscallConn()
	fd := -1
	if err == nil {
		var dupErr error
		err = raw.Control(func(original uintptr) {
			fd, dupErr = unix.FcntlInt(original, unix.F_DUPFD_CLOEXEC, 0)
		})
		if err == nil {
			err = dupErr
		}
	}
	if err == nil {
		err = unix.SetNonblock(fd, true)
	}
	_ = master.Close()
	if err != nil {
		if fd >= 0 {
			_ = unix.Close(fd)
		}
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "yoloctl-pty")
	// A successful deadline operation proves registration with Go's poller.
	// Refuse an uninterruptible fallback if a platform cannot register its PTY.
	if err := f.SetDeadline(time.Time{}); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

// winsize has 16-bit pixel fields. Unknown is preferable to truncation when
// a valid large grid exceeds them; CSI size reports retain the full geometry.
func ptyPixels(g terminal.Geometry) (uint16, uint16) {
	x, y := g.Cols*g.CellWidth, g.Rows*g.CellHeight
	if x > 65535 || y > 65535 {
		return 0, 0
	}
	return uint16(x), uint16(y)
}

// resizePTYGeometry avoids Fd, which can turn a pollable File back into blocking I/O.
func resizePTYGeometry(master *os.File, g terminal.Geometry) error {
	if !g.Valid() {
		return errors.New("invalid PTY geometry")
	}
	raw, err := master.SyscallConn()
	if err != nil {
		return err
	}
	var resizeErr error
	err = raw.Control(func(fd uintptr) {
		x, y := ptyPixels(g)
		resizeErr = unix.IoctlSetWinsize(int(fd), unix.TIOCSWINSZ, &unix.Winsize{Row: uint16(g.Rows), Col: uint16(g.Cols), Xpixel: x, Ypixel: y})
	})
	if err != nil {
		return err
	}
	return resizeErr
}
