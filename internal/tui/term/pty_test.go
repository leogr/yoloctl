package term

import (
	"bytes"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/leogr/yoloctl/internal/terminal"
	xterm "golang.org/x/term"
)

func TestPTYCloseInterruptsIOWhileSlaveStaysOpen(t *testing.T) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = slave.Close() }()
	if _, err := xterm.MakeRaw(int(slave.Fd())); err != nil {
		_ = master.Close()
		t.Fatal(err)
	}
	master, err = pollablePTY(master)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = master.Close() }()
	// Resize must preserve the pollable file behavior, too.
	if err := resizePTYGeometry(master, terminal.Geometry{Rows: 12, Cols: 40}); err != nil {
		t.Fatal(err)
	}
	readDone, writeDone := make(chan error, 1), make(chan error, 1)
	go func() { _, err := master.Read(make([]byte, 1)); readDone <- err }()
	go func() { _, err := master.Write(bytes.Repeat([]byte{'x'}, maxInputBytes)); writeDone <- err }()
	select {
	case err := <-readDone:
		t.Fatalf("read finished before close: %v", err)
	case err := <-writeDone:
		t.Fatalf("write finished before close: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	// Close in another goroutine so even a regression in Close itself fails
	// within the test deadline. Keeping slave open makes EOF an invalid excuse.
	closed := make(chan error, 1)
	go func() { closed <- master.Close() }()
	for _, done := range []<-chan error{closed, readDone, writeDone} {
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("master Close failed to interrupt PTY I/O with a live slave")
		}
	}
}
