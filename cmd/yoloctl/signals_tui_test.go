package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"reflect"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/leogr/yoloctl/internal/tui"
	"golang.org/x/term"
)

// A real renderer and the CLI's real signal context must shut down together.
// Both used to subscribe to SIGTERM: Bubble Tea could block sending QuitMsg
// after the context had stopped its event loop, preventing terminal cleanup.
func TestTUISignalShutdown(t *testing.T) {
	if mode := os.Getenv("YOLOCTL_TEST_TUI_SIGNAL"); mode != "" {
		ctx, stop := commandSignalContext()
		defer stop()
		if mode == "attached" {
			detached, err := tui.RunAttached(ctx, []string{"-test.run=^TestSignalViewPayload$"})
			if err != nil || !detached {
				t.Fatalf("detach=%t error=%v", detached, err)
			}
			fmt.Println("DETACHED_CLEANLY")
		} else {
			if err := tui.Run(ctx); err != nil {
				t.Fatal(err)
			}
			fmt.Println("TUI_CLOSED_CLEANLY")
		}
		// Let the parent inspect restored termios before Darwin revokes the
		// controlling terminal on session-leader exit. Use a private pipe:
		// terminal input must not compete with the renderer's stdin reader.
		release := os.NewFile(3, "signal-test-release")
		defer func() { _ = release.Close() }()
		if _, err := io.ReadFull(release, make([]byte, 1)); err != nil {
			t.Fatalf("parent acknowledgment: %v", err)
		}
		// Force output after the renderer's completion marker to exceed a PTY
		// buffer. The parent must keep draining until exit, even on Linux where
		// the test runner's small final PASS normally fits without a reader.
		if _, err := os.Stdout.Write(bytes.Repeat([]byte("shutdown trailer\n"), 4096)); err != nil {
			t.Fatalf("shutdown trailer: %v", err)
		}
		return
	}
	for _, mode := range []string{"attached", "palette"} {
		for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP, syscall.SIGQUIT} {
			t.Run(fmt.Sprintf("%s/%s", mode, sig), func(t *testing.T) {
				testTUISignalShutdown(t, mode, sig)
			})
		}
	}
}

// The attached frontend re-executes its binary under a second PTY. Keep that
// local client alive until the frontend detaches; no Lima/agent is involved.
func TestSignalViewPayload(t *testing.T) {
	if os.Getenv("YOLOCTL_TEST_TUI_SIGNAL") != "attached" {
		return
	}
	fmt.Print("ATTACHED_READY")
	_, _ = io.Copy(io.Discard, os.Stdin)
}

func testTUISignalShutdown(t *testing.T, mode string, sig syscall.Signal) {
	t.Helper()
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = master.Close(); _ = slave.Close() }()
	if err := pty.Setsize(master, &pty.Winsize{Rows: 24, Cols: 80}); err != nil {
		t.Fatal(err)
	}
	before, err := term.GetState(int(slave.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestTUISignalShutdown$", "-test.timeout=8s")
	releaseReader, releaseWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = releaseReader.Close(); _ = releaseWriter.Close() }()
	child.Env = append(os.Environ(), "YOLOCTL_TEST_TUI_SIGNAL="+mode, "XDG_CONFIG_HOME="+t.TempDir(), "TERM=xterm-256color", "YOLOCTL_TUI=", "GOMAXPROCS=2")
	child.Stdin, child.Stdout, child.Stderr = slave, slave, slave
	child.ExtraFiles = []*os.File{releaseReader}
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	_ = releaseReader.Close()
	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	t.Cleanup(func() { _ = child.Process.Kill() })
	// Use a separate copy of the descriptor for interruptible reads. The
	// retained slave permits inspecting restored termios even on Darwin.
	fd, err := syscall.Dup(int(master.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	syscall.CloseOnExec(fd)
	if err := syscall.SetNonblock(fd, true); err != nil {
		_ = syscall.Close(fd)
		t.Fatal(err)
	}
	reader := os.NewFile(uintptr(fd), "signal-test-pty")
	defer func() { _ = reader.Close() }()
	if err := reader.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var captured bytes.Buffer
	buf := make([]byte, 32768)
	ready, finished := "ATTACHED_READY", "DETACHED_CLEANLY"
	if mode == "palette" {
		ready, finished = "yoloctl", "TUI_CLOSED_CLEANLY"
	}
	for !bytes.Contains(captured.Bytes(), []byte(ready)) {
		n, err := reader.Read(buf)
		captured.Write(buf[:n])
		if err != nil {
			t.Fatalf("renderer not ready: %v; output=%q", err, captured.String())
		}
	}
	if err := child.Process.Signal(sig); err != nil {
		t.Fatal(err)
	}
	shutdownStart := captured.Len()
	// Continue draining while it shuts down. A test must not create its own
	// blocked stdout and misdiagnose that as a signal deadlock.
	for !bytes.Contains(captured.Bytes(), []byte(finished)) {
		n, err := reader.Read(buf)
		captured.Write(buf[:n])
		if err != nil {
			t.Fatalf("shutdown did not finish: %v; output=%q", err, captured.String())
		}
	}
	after, err := term.GetState(int(slave.Fd()))
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("terminal settings not restored: %v", err)
	}
	for _, reset := range []string{
		"\x1b[?1049l", "\x1b[?1002l", "\x1b[?1003l", "\x1b[?1006l", "\x1b[?1016l",
		"\x1b[?1004l", "\x1b[?2004l", "\x1b[>4m", "\x1b[<1u",
	} {
		if !bytes.Contains(captured.Bytes()[shutdownStart:], []byte(reset)) {
			t.Errorf("missing terminal reset %q", reset)
		}
	}
	if _, err := releaseWriter.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	// A renderer return is not process exit: the helper/test runner still
	// writes to stdout, and Darwin's session-leader exit waits for pending
	// terminal output to drain. Keep reading while Wait runs. Retaining slave
	// above prevents EOF on Linux, so explicitly stop and join this reader.
	drained := make(chan struct{})
	go func() {
		_, _ = io.Copy(&captured, reader)
		close(drained)
	}()
	select {
	case err = <-done:
	case <-time.After(3 * time.Second):
		err = fmt.Errorf("renderer returned but process stayed alive")
	}
	_ = reader.Close()
	<-drained
	if err != nil {
		output := captured.Bytes()
		if len(output) > 4096 {
			output = output[len(output)-4096:]
		}
		t.Fatalf("signal exit: %v; output tail=%q", err, output)
	}
}
