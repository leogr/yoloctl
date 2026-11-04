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
