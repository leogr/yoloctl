//go:build !windows

package sshtransport

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"

	"golang.org/x/crypto/ssh"
	"golang.org/x/sys/unix"
)

type terminalWindow struct{ Cols, Rows, Width, Height uint32 }

func windowSize(input *os.File) (terminalWindow, error) {
	w, err := unix.IoctlGetWinsize(int(input.Fd()), unix.TIOCGWINSZ)
	if err != nil {
		return terminalWindow{}, err
	}
	return terminalWindow{uint32(w.Col), uint32(w.Row), uint32(w.Xpixel), uint32(w.Ypixel)}, nil
}

func requestPTY(s *ssh.Session, input *os.File, name string) error {
	w, err := windowSize(input)
	if err != nil {
		return err
	}
	var modes []byte
	for _, mode := range []struct {
		Code  byte
		Value uint32
	}{{ssh.ECHO, 1}, {ssh.TTY_OP_ISPEED, 38400}, {ssh.TTY_OP_OSPEED, 38400}} {
		modes = append(modes, ssh.Marshal(mode)...)
	}
	modes = append(modes, 0)
	// x/crypto's convenience PTY/resize methods invent eight pixels per cell.
	// Forward only measured TIOCGWINSZ pixels; zero must remain unknown.
	req := struct {
		Name                      string
		Cols, Rows, Width, Height uint32
		Modes                     string
	}{name, w.Cols, w.Rows, w.Width, w.Height, string(modes)}
	ok, err := s.SendRequest("pty-req", true, ssh.Marshal(req))
	if err == nil && !ok {
		return errors.New("server rejected SSH PTY")
	}
	return err
}

func resizeSession(s *ssh.Session, input *os.File) func() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			case <-ch:
				size, err := windowSize(input)
				if err == nil {
					_, _ = s.SendRequest("window-change", false, ssh.Marshal(size))
				}
			}
		}
	}()
	return func() { signal.Stop(ch); close(done) }
}

func copyInput(ctx context.Context, dst io.Writer, src *os.File) error {
	fd, err := unix.FcntlInt(src.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	buf := make([]byte, 32<<10)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		n, err := unix.Poll(poll, 50)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
		if n == 0 {
			continue
		}
		n, err = unix.Read(fd, buf)
		if errors.Is(err, unix.EINTR) || errors.Is(err, unix.EAGAIN) {
			continue
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
		if _, err = dst.Write(buf[:n]); err != nil {
			return err
		}
	}
}

// ProcessConn connects to an existing, authorized byte-stream proxy using an
// inherited socket pair. It adds no listener, host tool or shell interpretation.
func ProcessConn(ctx context.Context, cmd *exec.Cmd) (net.Conn, error) {
	syscall.ForkLock.RLock()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		syscall.ForkLock.RUnlock()
		return nil, err
	}
	unix.CloseOnExec(fds[0])
	unix.CloseOnExec(fds[1])
	syscall.ForkLock.RUnlock()
	parent := os.NewFile(uintptr(fds[0]), "ssh-proxy-parent")
	child := os.NewFile(uintptr(fds[1]), "ssh-proxy-child")
	conn, err := net.FileConn(parent)
	_ = parent.Close()
	if err != nil {
		_ = child.Close()
		return nil, err
	}
	cmd.Stdin, cmd.Stdout = child, child
	if err = cmd.Start(); err != nil {
		_ = conn.Close()
		_ = child.Close()
		return nil, err
	}
	_ = child.Close()
	p := &processConn{Conn: conn, cmd: cmd, done: make(chan struct{})}
	go func() { _ = cmd.Wait(); close(p.done) }()
	go func() {
		select {
		case <-ctx.Done():
			_ = p.Close()
		case <-p.done:
		}
	}()
	return p, nil
}

type processConn struct {
	net.Conn
	cmd  *exec.Cmd
	done chan struct{}
	once sync.Once
}

func (p *processConn) Close() error {
	p.once.Do(func() {
		_ = p.Conn.Close()
		_ = p.cmd.Process.Kill()
		<-p.done
	})
	return nil
}
