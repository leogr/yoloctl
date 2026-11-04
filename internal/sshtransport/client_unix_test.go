//go:build !windows

package sshtransport

import (
	"context"
	"io"
	"os"
	"reflect"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/crypto/ssh"
	"golang.org/x/term"
)

func TestPTYSizeResizeAndTerminalRestoration(t *testing.T) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = master.Close(); _ = slave.Close() }()
	if err = pty.Setsize(master, &pty.Winsize{Cols: 80, Rows: 24}); err != nil {
		t.Fatal(err)
	}
	before, err := term.GetState(int(slave.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	initial := make(chan []byte, 1)
	resized := make(chan []byte, 1)
	conn, config := testServer(t, func(c ssh.Channel, requests <-chan *ssh.Request) {
		defer func() { _ = c.Close() }()
		for req := range requests {
			switch req.Type {
			case "pty-req":
				initial <- req.Payload
				_ = req.Reply(true, nil)
			case "exec":
				_ = req.Reply(true, nil)
			case "window-change":
				resized <- req.Payload
				_, _ = c.SendRequest("exit-status", false, ssh.Marshal(struct{ Code uint32 }{0}))
				return
			default:
				_ = req.Reply(false, nil)
			}
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Run(ctx, conn, config, "interactive", true, slave, io.Discard, io.Discard) }()
	var raw []byte
	select {
	case raw = <-initial:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var first struct {
		Term                      string
		Cols, Rows, Width, Height uint32
		Modes                     string
	}
	if err = ssh.Unmarshal(raw, &first); err != nil || first.Cols != 80 || first.Rows != 24 || first.Width != 0 || first.Height != 0 {
		t.Fatalf("initial PTY: %+v %v", first, err)
	}
	if err = pty.Setsize(master, &pty.Winsize{Cols: 120, Rows: 40, X: 1200, Y: 880}); err != nil {
		t.Fatal(err)
	}
	// Allow the initial PTY reply to reach the client before delivering resize.
	for {
		_ = syscall.Kill(os.Getpid(), syscall.SIGWINCH)
		select {
		case raw = <-resized:
			goto received
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
received:
	var resize struct{ Cols, Rows, Width, Height uint32 }
	if err = ssh.Unmarshal(raw, &resize); err != nil || resize.Cols != 120 || resize.Rows != 40 || resize.Width != 1200 || resize.Height != 880 {
		t.Fatalf("resize: %+v %v", resize, err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	after, err := term.GetState(int(slave.Fd()))
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("terminal state not restored: %v", err)
	}
}
