package spacelock

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestContendedLockReportsWaitAndCancels(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	release, err := Acquire(t.Context(), "work", true)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	notices := 0
	_, err = AcquireWithWait(ctx, "work", true, func() { notices++; cancel() })
	if !errors.Is(err, context.Canceled) || notices != 1 {
		t.Fatalf("notice=%d error=%v", notices, err)
	}
}

func TestExistingLifecycleLockDoesNotCreateStorage(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing")
	t.Setenv("YOLOCTL_HOME", root)
	if _, err := AcquireExistingWithWait(t.Context(), "absent", true, nil); err == nil {
		t.Fatal("missing stable lock accepted")
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("read-only lock initialized storage")
	}
}

func TestTryAcquireTakesOnceAndReportsOnlyContentionAsBusy(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	release, err := TryAcquire(t.Context(), "work", true)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if _, err := TryAcquire(t.Context(), "work", true); !errors.Is(err, ErrBusy) || time.Since(started) > time.Second {
		t.Fatalf("contention: %v after %v", err, time.Since(started))
	}
	release()
	again, err := TryAcquire(t.Context(), "work", true)
	if err != nil {
		t.Fatalf("free again: %v", err)
	}
	again()
	// Another failure (storage that cannot be opened) is not contention.
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", file)
	t.Setenv("YOLOCTL_HOME", filepath.Join(file, "home"))
	if _, err := TryAcquire(t.Context(), "work", true); err == nil || errors.Is(err, ErrBusy) {
		t.Fatalf("open failure reported as %v", err)
	}
}
