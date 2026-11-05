package installer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// A spurious ENOENT from creating install.lock (macOS, two installs racing on
// a fresh store) is retried a bounded number of times; any other error is
// returned at once, and the checks after the open are unchanged.
func TestLockRetriesOnlySpuriousENOENT(t *testing.T) {
	store, err := openDir(filepath.Join(physicalTemp(t), "store"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	real := openLockFile
	t.Cleanup(func() { openLockFile = real })
	inject := func(failures int, failure error) *int {
		calls := 0
		openLockFile = func(dirfd int) (int, error) {
			calls++
			if calls <= failures {
				return -1, failure
			}
			return real(dirfd)
		}
		return &calls
	}

	calls := inject(2, unix.ENOENT)
	unlock, err := lock(t.Context(), store)
	if err != nil || *calls != 3 {
		t.Fatalf("two spurious ENOENT then success: %v after %d opens", err, *calls)
	}
	unlock()
	if info, err := os.Lstat(filepath.Join(store.path, "install.lock")); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("lock file: %v", err)
	}

	calls = inject(lockOpenAttempts, unix.ENOENT)
	if _, err := lock(t.Context(), store); !errors.Is(err, unix.ENOENT) || *calls != lockOpenAttempts {
		t.Fatalf("persistent ENOENT: %v after %d opens", err, *calls)
	}

	calls = inject(1, unix.EACCES)
	if _, err := lock(t.Context(), store); !errors.Is(err, unix.EACCES) || *calls != 1 {
		t.Fatalf("another error is not retried: %v after %d opens", err, *calls)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	calls = inject(1, unix.ENOENT)
	if _, err := lock(ctx, store); !errors.Is(err, context.Canceled) || *calls != 1 {
		t.Fatalf("a cancelled retry stops: %v after %d opens", err, *calls)
	}
}
