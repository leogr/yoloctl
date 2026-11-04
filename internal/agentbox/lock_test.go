package agentbox

import (
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/layout"
)

func TestLockConfig_Acquires(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	release, err := LockConfig("default", "coder")
	if err != nil {
		t.Fatalf("LockConfig: %v", err)
	}
	defer release()
	// Lock file exists on disk after acquisition.
	dir, _ := layout.BoxesDir("default")
	if _, err := exec.Command("test", "-f", filepath.Join(dir, "coder.lock")).Output(); err != nil {
		// `test -f` exits non-zero when file missing. Use Go's
		// os.Stat for a cleaner error.
		t.Errorf("lock file not created at expected path under %s", dir)
	}
}

func TestLockConfig_ReleaseLetsSecondAcquire(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	release, err := LockConfig("default", "coder")
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}
	release()
	// After release the next acquire should succeed immediately.
	release2, err := LockConfig("default", "coder")
	if err != nil {
		t.Fatalf("second lock after release: %v", err)
	}
	release2()
}

func TestLockConfig_BlocksConcurrentProcess(t *testing.T) {
	// flock is process-scoped, not goroutine-scoped — locking from
	// the same process twice succeeds. Verify cross-process blocking
	// by spawning `flock(1)` to hold the same file, then trying to
	// LockConfig from this process with a deadline. The acquisition
	// should NOT complete while the external holder is alive.
	if _, err := exec.LookPath("flock"); err != nil {
		t.Skip("flock(1) not on PATH; skipping cross-process lock test")
	}
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	abDir, _ := layout.BoxesDir("default")
	if err := exec.Command("mkdir", "-p", abDir).Run(); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(abDir, "coder.lock")
	// Hold the lock externally for 1 second.
	holder := exec.Command("flock", "-x", lockPath, "-c", "sleep 1")
	if err := holder.Start(); err != nil {
		t.Fatalf("start external flock holder: %v", err)
	}
	defer func() { _ = holder.Wait() }()
	// Give the external holder a moment to acquire.
	time.Sleep(100 * time.Millisecond)

	// Try to acquire from this process in a goroutine; should block.
	var acquired atomic.Bool
	done := make(chan struct{})
	go func() {
		release, err := LockConfig("default", "coder")
		if err == nil {
			acquired.Store(true)
			release()
		}
		close(done)
	}()

	select {
	case <-done:
		if acquired.Load() {
			t.Errorf("LockConfig returned while external holder still owned the lock")
		}
		// If it completed without acquiring (error), surface — but
		// the syscall path blocks rather than erroring, so this
		// shouldn't happen.
	case <-time.After(300 * time.Millisecond):
		// Still blocked after 300ms while external holder owns the
		// lock — expected. Wait for the external holder to release,
		// then verify our goroutine eventually acquires.
	}
	// External holder exits after ~1s total; the goroutine should
	// then acquire. Give it generous slack.
	select {
	case <-done:
		if !acquired.Load() {
			t.Errorf("LockConfig completed but did not acquire (likely an unexpected error)")
		}
	case <-time.After(2 * time.Second):
		t.Errorf("LockConfig did not acquire even after external holder released")
	}
}

func TestLockConfig_RejectsBadName(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	// Invalid name should fail through layout.BoxPath's validation.
	// path before any file work happens. ".." is the classic
	// path-traversal probe.
	_, err := LockConfig("default", "..")
	if err == nil {
		t.Fatal("expected error for invalid name, got nil")
	}
	// Validation happens inside layout.BoxLockPath before any file
	// work. Acceptable as long as some error surfaces.
	if !strings.Contains(err.Error(), "..") && !strings.Contains(err.Error(), "lock") {
		t.Errorf("error %q should mention either the bad name or the lock file", err.Error())
	}
}
