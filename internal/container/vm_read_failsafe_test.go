package container

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// hangingVMFixture is a client whose VM command matching pattern never
// answers, and that fails every other command.
func hangingVMFixture(t *testing.T, pattern string) *Client {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LIMA_HOME", t.TempDir())
	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	script := "#!/bin/sh\ncase \"$*\" in\n*'" + pattern + "'*) exec sleep 30;;\nesac\nexit 97\n"
	if err := os.WriteFile(filepath.Join(dir, "limactl"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return NewWithOutput("fixture", io.Discard, io.Discard)
}

func shortVMReadFailSafe(t *testing.T, d time.Duration) {
	t.Helper()
	old := vmReadFailSafe
	vmReadFailSafe = d
	t.Cleanup(func() { vmReadFailSafe = old })
}

// ended runs read and fails the test if it outlives a short end.
func ended(t *testing.T, read func() error) error {
	t.Helper()
	start := time.Now()
	err := read()
	if time.Since(start) > 20*time.Second {
		t.Fatal("read outlived its end", time.Since(start))
	}
	return err
}

func TestVMReadFailSafeDefault(t *testing.T) {
	if vmReadFailSafe != 2*time.Minute {
		t.Fatal("VM read fail-safe changed", vmReadFailSafe)
	}
}

// A pool read that never answers ends with today's error at the fail-safe or
// at an earlier caller deadline.
func TestWorkloadPoolReadEndsWithFailSafeOrCaller(t *testing.T) {
	c := hangingVMFixture(t, "cat /sys/fs/cgroup/")
	const unavailable = "VM workload memory pool is unavailable: "
	shortVMReadFailSafe(t, 200*time.Millisecond)
	if err := ended(t, func() error { return c.RequireWorkloadPool(t.Context()) }); err == nil || !strings.HasPrefix(err.Error(), unavailable) {
		t.Fatal("unanswered pool read did not end at the fail-safe", err)
	}
	vmReadFailSafe = time.Minute
	caller, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	if err := ended(t, func() error { return c.RequireWorkloadPool(caller) }); err == nil || !strings.HasPrefix(err.Error(), unavailable) {
		t.Fatal("earlier caller deadline did not end the pool read", err)
	}
}

// A projection inspect ends at its own fail-safe or an earlier caller end.
func TestNativeOperatorProjectionEndsWithFailSafeOrCaller(t *testing.T) {
	c := hangingVMFixture(t, "nerdctl inspect --mode native")
	const settlement = "native mount inventory command/pipe settlement failed: "
	shortVMReadFailSafe(t, 200*time.Millisecond)
	err := ended(t, func() error {
		_, err := c.NativeOperatorProjection(t.Context(), operatorProjectionCID, "selected")
		return err
	})
	if !errors.Is(err, context.DeadlineExceeded) || !strings.HasPrefix(err.Error(), settlement) {
		t.Fatal("unanswered inspect did not end at the fail-safe", err)
	}
	vmReadFailSafe = time.Minute
	caller, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	err = ended(t, func() error {
		_, err := c.NativeOperatorProjection(caller, operatorProjectionCID, "selected")
		return err
	})
	if !errors.Is(err, context.DeadlineExceeded) || !strings.HasPrefix(err.Error(), settlement) {
		t.Fatal("earlier caller deadline did not end the inspect", err)
	}
}

// The pool read owns its stdout: when cat exits but a descendant keeps the
// pipe, the caller's cancellation ends the wait at once and no partial pool
// is accepted.
func TestWorkloadPoolReadHeldPipeEndsWithCaller(t *testing.T) {
	c, mark, _ := workloadPoolHeldPipeFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		for ctx.Err() == nil {
			if pid := heldPoolFixturePID(mark); pid > 0 && syscall.Kill(pid, 0) == syscall.ESRCH {
				cancel()
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	start := time.Now()
	err := c.RequireWorkloadPool(ctx)
	if !errors.Is(err, context.Canceled) || time.Since(start) > 3*time.Second {
		t.Fatal("held pool pipe outlived the caller or a partial pool was accepted", err, time.Since(start))
	}
}

func TestWorkloadPoolReadHeldPipeEndsWithFailSafe(t *testing.T) {
	c, mark, holder := workloadPoolHeldPipeFixture(t)
	shortVMReadFailSafe(t, time.Second)
	start := time.Now()
	err := c.RequireWorkloadPool(t.Context())
	leaderPID, holderPID := heldPoolFixturePID(mark), heldPoolFixturePID(holder)
	if !errors.Is(err, context.DeadlineExceeded) || t.Context().Err() != nil || time.Since(start) > 4*time.Second ||
		leaderPID <= 0 || syscall.Kill(leaderPID, 0) != syscall.ESRCH ||
		holderPID <= 0 || syscall.Kill(holderPID, 0) != nil {
		t.Fatal("fail-safe did not end an owned pipe after adapter exit", err, leaderPID, holderPID, time.Since(start))
	}
}

func workloadPoolHeldPipeFixture(t *testing.T) (*Client, string, string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LIMA_HOME", t.TempDir())
	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	mark := filepath.Join(dir, "leader")
	holder := filepath.Join(dir, "holder")
	script := "#!/bin/sh\ncase \"$*\" in\n*'cat /sys/fs/cgroup/'*) (exec 2>/dev/null; sleep 30) & echo $! > '" + holder + "'; printf partial; echo $$ > '" + mark + "'; exit 0;;\nesac\nexit 97\n"
	if err := os.WriteFile(filepath.Join(dir, "limactl"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if pid := heldPoolFixturePID(holder); pid > 0 {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	return NewWithOutput("fixture", io.Discard, io.Discard), mark, holder
}

func heldPoolFixturePID(path string) int {
	raw, _ := os.ReadFile(path)
	pid, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	return pid
}
