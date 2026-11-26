package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"reflect"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/leogr/yoloctl/internal/controller"
	vmhost "github.com/leogr/yoloctl/internal/vm"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func TestControllerReadInheritsCallerDeadline(t *testing.T) {
	for _, tc := range []struct {
		name   string
		budget time.Duration
	}{
		{name: "no deadline"},
		{name: "earlier deadline", budget: time.Second},
		{name: "later deadline", budget: time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.budget > 0 {
				var stop context.CancelFunc
				ctx, stop = context.WithTimeout(ctx, tc.budget)
				defer stop()
			}
			root := &cobra.Command{}
			root.SetContext(ctx)
			root.SetIn(bytes.NewReader(nil))
			root.SetOut(io.Discard)
			root.SetErr(io.Discard)
			want, wantOK := ctx.Deadline()
			calls := 0
			target := controllerTarget{Space: "work", Operator: true, Call: func(ctx context.Context, _ controller.Request, streams controller.ClientIO) error {
				calls++
				if got, ok := ctx.Deadline(); ok != wantOK || !got.Equal(want) {
					t.Fatalf("deadline = %v, %t, want %v, %t", got, ok, want, wantOK)
				}
				if streams.Signals != nil || streams.In != nil {
					t.Fatal("ordinary read set up interactive forwarding")
				}
				cancel()
				return ctx.Err()
			}}
			err := callControllerTerminal(root, target, controller.Request{Method: "box.list", Space: "work"})
			if !errors.Is(err, context.Canceled) || calls != 1 {
				t.Fatalf("read returned %v after %d calls", err, calls)
			}
		})
	}
}

func TestControllerSpaceLookupInheritsCallerDeadline(t *testing.T) {
	boot := imageUpdateFixture(t)
	identity, err := controller.ExecutableIdentity()
	if err != nil {
		t.Fatal(err)
	}
	boot.Bundle = identity
	if err := boot.Save(); err != nil {
		t.Fatal(err)
	}
	lookup := controllerSpaceLookup
	t.Cleanup(func() { controllerSpaceLookup = lookup })
	for _, budget := range []time.Duration{0, time.Second, time.Minute} {
		t.Run(budget.String(), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if budget > 0 {
				var stop context.CancelFunc
				ctx, stop = context.WithTimeout(ctx, budget)
				defer stop()
			}
			want, wantOK := ctx.Deadline()
			controllerSpaceLookup = func(probe context.Context, name string) (vmhost.Instance, bool, error) {
				if got, ok := probe.Deadline(); ok != wantOK || !got.Equal(want) || name != boot.Name {
					t.Fatalf("lookup %q deadline %v, %t, want %q %v, %t", name, got, ok, boot.Name, want, wantOK)
				}
				cancel()
				return vmhost.Instance{}, false, probe.Err()
			}
			if _, err := checkControllerSpace(ctx, boot.Name); !errors.Is(err, context.Canceled) {
				t.Fatalf("lookup returned %v", err)
			}
		})
	}
}

func TestControllerReadTerminal(t *testing.T) {
	if mode := os.Getenv("YOLOCTL_TEST_CONTROLLER_READ_PTY"); mode != "" {
		runControllerReadPTYChild(t, mode)
		return
	}
	for _, mode := range []string{"interrupt", "resize"} {
		t.Run(mode, func(t *testing.T) { testControllerReadPTY(t, mode) })
	}
}

const controllerReadPTYOutput = "row\r\nnext\r\n"

func testControllerReadPTY(t *testing.T, mode string) {
	t.Helper()
	master, slave, err := pty.Open()
	if err != nil {
		t.Skipf("PTY unavailable: %v", err)
	}
	defer func() { _ = master.Close(); _ = slave.Close() }()
	if err := pty.Setsize(master, &pty.Winsize{Rows: 24, Cols: 80}); err != nil {
		t.Fatal(err)
	}
	before, err := term.GetState(int(slave.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	progress, childProgress, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = progress.Close(); _ = childProgress.Close() }()
	childRelease, release, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = childRelease.Close(); _ = release.Close() }()
	var stderr lockedBuffer
	child := exec.Command(os.Args[0], "-test.run=^TestControllerReadTerminal$", "-test.timeout=10s")
	child.Env = append(os.Environ(), "YOLOCTL_TEST_CONTROLLER_READ_PTY="+mode, "YOLOCTL_HOME="+t.TempDir(), "YOLOCTL_TUI=", "TERM=xterm-256color")
	child.Stdin, child.Stdout, child.Stderr = slave, slave, &stderr
	child.ExtraFiles = []*os.File{childProgress, childRelease}
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	_ = childProgress.Close()
	_ = childRelease.Close()
	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	waited := false
	defer func() {
		if !waited {
			// Release the tty before reaping a child that may be blocked
			// in the controlling terminal's exit-time drain on Darwin.
			_ = master.Close()
			_ = slave.Close()
			_ = child.Process.Kill()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Errorf("could not reap PTY child after closing the terminal")
			}
		}
	}()
	readMarker := func(want byte) {
		t.Helper()
		if err := progress.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
			t.Fatal(err)
		}
		var marker [1]byte
		if _, err := io.ReadFull(progress, marker[:]); err != nil || marker[0] != want {
			t.Fatalf("progress = %q, %v, want %q, child stderr %q", marker[0], err, want, stderr.String())
		}
	}
	readMarker('H')
	if mode == "interrupt" {
		// The peer read our hello and is withholding its own.
		if _, err := master.Write([]byte{3}); err != nil {
			t.Fatal(err)
		}
	} else {
		readMarker('R') // The peer completed hello and received box.list.
		if err := pty.Setsize(master, &pty.Winsize{Rows: 34, Cols: 92}); err != nil {
			t.Fatal(err)
		}
		if err := child.Process.Signal(syscall.SIGWINCH); err != nil {
			t.Fatal(err)
		}
	}
	readMarker('C')
	fd, err := syscall.Dup(int(master.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	syscall.CloseOnExec(fd)
	if err := syscall.SetNonblock(fd, true); err != nil {
		_ = syscall.Close(fd)
		t.Fatal(err)
	}
	reader := os.NewFile(uintptr(fd), "read-PTY-output")
	var drained chan struct{}
	defer func() {
		_ = reader.Close()
		if drained != nil {
			<-drained
		}
	}()
	if mode == "resize" {
		if err := reader.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		got := make([]byte, len(controllerReadPTYOutput))
		if _, err := io.ReadFull(reader, got); err != nil || string(got) != controllerReadPTYOutput {
			t.Fatalf("PTY output %q, %v, want exactly %q", got, err, controllerReadPTYOutput)
		}
		if err := reader.SetReadDeadline(time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
	after, err := term.GetState(int(slave.Fd()))
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("terminal state not preserved: %v", err)
	}
	// The child can now exit. Keep reading its test runner output while Darwin
	// drains and revokes the session leader's controlling terminal.
	drained = make(chan struct{})
	go func() {
		defer close(drained)
		_, _ = io.Copy(io.Discard, reader)
	}()
	if _, err := release.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		waited = true
		if err != nil {
			t.Fatalf("child did not exit cleanly: %v, stderr %q", err, stderr.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("interrupted command did not exit")
	}
}

func runControllerReadPTYChild(t *testing.T, mode string) {
	t.Helper()
	progress := os.NewFile(3, "read-PTY-progress")
	release := os.NewFile(4, "read-PTY-release")
	defer func() { _ = progress.Close(); _ = release.Close() }()
	ctx, stop := commandSignalContext()
	defer stop()
	calls := 0
	peerDone := make(chan error, 1)
	controllerTargetForCommand = func(context.Context, *cobra.Command) (controllerTarget, error) {
		return controllerTarget{Space: "work", Operator: true, Call: func(ctx context.Context, req controller.Request, streams controller.ClientIO) error {
			calls++
			if calls != 1 || req.Method != "box.list" || !req.Terminal || req.Cols != 80 || req.Rows != 24 {
				return fmt.Errorf("unexpected routed request: call %d, %+v", calls, req)
			}
			conn, peer := net.Pipe()
			go func() {
				defer func() { _ = peer.Close() }()
				wire := controller.NewWire(peer, peer)
				f, err := wire.Read()
				if err != nil || f.Type != "hello" {
					peerDone <- fmt.Errorf("first frame %q: %w", f.Type, err)
					return
				}
				if _, err := progress.Write([]byte{'H'}); err != nil {
					peerDone <- err
					return
				}
				if mode == "interrupt" {
					if f, err := wire.Read(); !errors.Is(err, io.EOF) {
						peerDone <- fmt.Errorf("after hello received %q, error %v", f.Type, err)
						return
					}
					peerDone <- nil
					return
				}
				peerDone <- serveControllerReadResize(wire, f.Hello, progress)
			}()
			hello := controller.Hello{Protocol: controller.ProtocolVersion, Bundle: "test", Space: "work", Generation: "generation"}
			return controller.Call(ctx, conn, hello, req, streams)
		}}, nil
	}
	root := newClientRootCmd()
	root.SetArgs([]string{"box", "list"})
	err := root.ExecuteContext(ctx)
	if calls != 1 || mode == "interrupt" && (err == nil || !errors.Is(ctx.Err(), context.Canceled)) || mode == "resize" && err != nil {
		t.Fatalf("read returned %v with context %v and %d calls", err, ctx.Err(), calls)
	}
	if err := <-peerDone; err != nil {
		t.Fatal(err)
	}
	if _, err := progress.Write([]byte{'C'}); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(release, make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
}

func serveControllerReadResize(wire *controller.Wire, hello *controller.Hello, progress io.Writer) error {
	if err := wire.Write(controller.Frame{Type: "hello", Hello: hello}); err != nil {
		return err
	}
	f, err := wire.Read()
	if err != nil || f.Type != "request" || f.Request == nil || f.Request.Method != "box.list" {
		return fmt.Errorf("after server hello received %q, error %v", f.Type, err)
	}
	if _, err := progress.Write([]byte{'R'}); err != nil {
		return err
	}
	for {
		f, err := wire.Read()
		if err != nil {
			return err
		}
		if f.Type == "eof" {
			continue
		}
		if f.Type != "resize" || f.Cols != 92 || f.Rows != 34 {
			return fmt.Errorf("expected 92x34 resize, got %q %dx%d", f.Type, f.Cols, f.Rows)
		}
		break
	}
	if err := wire.Write(controller.Frame{Type: "stdout", Data: []byte(controllerReadPTYOutput)}); err != nil {
		return err
	}
	return wire.Write(controller.Frame{Type: "exit"})
}
