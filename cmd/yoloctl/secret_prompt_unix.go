//go:build linux || darwin

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// readHiddenLine reads one line from the terminal f with echo off. Echo is
// turned off before the prompt is written, so input typed or pasted right
// after the prompt is never shown; canonical input and signals stay on. The
// saved terminal settings are restored on every return, including when ctx
// ends first (the CLI cancels it on Ctrl+C), and by then nothing is still
// reading the terminal.
func readHiddenLine(ctx context.Context, f *os.File, prompt string, w io.Writer, limit int64) (string, error) {
	fd := int(f.Fd())
	saved, err := unix.IoctlGetTermios(fd, ioctlGetTermios)
	if err != nil {
		return "", err
	}
	hidden := *saved
	hidden.Lflag &^= unix.ECHO
	hidden.Lflag |= unix.ICANON | unix.ISIG
	hidden.Iflag |= unix.ICRNL
	if err := unix.IoctlSetTermios(fd, ioctlSetTermios, &hidden); err != nil {
		return "", err
	}
	defer func() { _ = unix.IoctlSetTermios(fd, ioctlSetTermios, saved) }()
	_, _ = fmt.Fprint(w, prompt)
	defer func() { _, _ = fmt.Fprintln(w) }()
	// Read in this goroutine, polling a duplicate of the descriptor, so that
	// when ctx ends nothing is left reading the caller's terminal: the next
	// line stays for the next reader (the pattern of sshtransport.copyInput).
	dup, err := unix.FcntlInt(f.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return "", err
	}
	defer func() { _ = unix.Close(dup) }()
	// The duplicate shares the caller's file flags. Read without blocking, so
	// a line that Ctrl+C flushes between Poll and Read cannot block, and
	// restore the caller's flags on return.
	flags, err := unix.FcntlInt(uintptr(dup), unix.F_GETFL, 0)
	if err != nil {
		return "", err
	}
	if _, err := unix.FcntlInt(uintptr(dup), unix.F_SETFL, flags|unix.O_NONBLOCK); err != nil {
		return "", err
	}
	defer func() { _, _ = unix.FcntlInt(uintptr(dup), unix.F_SETFL, flags) }()
	var line []byte
	buf := make([]byte, 4096)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		ready, err := unix.Poll([]unix.PollFd{{Fd: int32(dup), Events: unix.POLLIN}}, 50)
		if errors.Is(err, unix.EINTR) || err == nil && ready == 0 {
			continue
		}
		if err != nil {
			return "", err
		}
		// A cancellation during Poll wins over a line that arrived meanwhile.
		if err := ctx.Err(); err != nil {
			return "", err
		}
		// In canonical mode one read returns at most one line.
		n, err := unix.Read(dup, buf)
		if errors.Is(err, unix.EINTR) || errors.Is(err, unix.EAGAIN) {
			continue
		}
		if err != nil {
			return "", err
		}
		// Never return a value once the command was cancelled: not at Enter,
		// at the size limit or at end of input.
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if n == 0 {
			return string(line), io.EOF
		}
		line = append(line, buf[:n]...)
		if bytes.IndexByte(buf[:n], '\n') >= 0 || int64(len(line)) >= limit {
			return string(line), nil
		}
	}
}
