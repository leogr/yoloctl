package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// Exercise real process signals in a child so signal.Notify cannot interfere
// with the test runner or other tests. A stream must receive its first signal
// without losing the connection that carries the detach hint and exit status.
func TestControllerProcessSignals(t *testing.T) {
	if mode := os.Getenv("YOLOCTL_TEST_SIGNALS"); mode != "" {
		ctx, stop := commandSignalContext()
		defer stop()
		forwarded := make(chan int, 1)
		if mode != "ordinary" {
			release := forwardCommandSignals(ctx, forwarded)
			defer release()
			if mode == "removed" {
				release()
			}
		}
		fmt.Println("ready")
		if mode == "stream" {
			select {
			case sig := <-forwarded:
				if sig != int(syscall.SIGTERM) || ctx.Err() != nil {
					t.Fatalf("first signal did not preserve stream: %d, %v", sig, ctx.Err())
				}
				fmt.Println("forwarded")
			case <-ctx.Done():
				t.Fatal("stream canceled before forwarding")
			}
		}
		<-ctx.Done()
		fmt.Println("canceled")
		return
	}
	for _, mode := range []string{"ordinary", "stream", "removed"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestControllerProcessSignals$")
			child.Env = append(os.Environ(), "YOLOCTL_TEST_SIGNALS="+mode)
			child.Stderr = os.Stderr
			stdout, err := child.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := child.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
			lines := bufio.NewScanner(stdout)
			expect := func(want string) {
				t.Helper()
				if !lines.Scan() || lines.Text() != want {
					t.Fatalf("wanted %q, got %q (%v)", want, lines.Text(), lines.Err())
				}
			}
			expect("ready")
			if err := child.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			if mode == "stream" {
				expect("forwarded")
				if err := child.Process.Signal(os.Interrupt); err != nil {
					t.Fatal(err)
				}
			}
			expect("canceled")
			if err := child.Wait(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
