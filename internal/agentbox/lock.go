package agentbox

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/leogr/yoloctl/internal/layout"
)

// LockConfig acquires an exclusive file lock on the agentbox's YAML
// path so concurrent yoloctl invocations don't race on the same
// agentbox. Returns a release function the caller must defer.
//
// Implementation uses flock(2) on a sibling .lock file (so the lock
// file does NOT live at the YAML path itself — that path may be
// rewritten atomically via tmp+rename, which would lose the lock).
// flock is process-scoped: separate yoloctl invocations are separate
// processes, so this is the right granularity. Within one process,
// the same fd can re-acquire — fine, we never need that.
//
// Lock files accumulate at
// $XDG_CONFIG_HOME/yoloctl/spaces/<space>/boxes/<name>.lock and persist
// across yoloctl invocations (cheap zero-byte files; the kernel cleans
// up advisory locks on process exit). Cleanup happens alongside Delete.
func LockConfig(space, name string) (release func(), err error) {
	lockPath, err := layout.BoxLockPath(space, name) // validates space + name
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(lockPath), dirMode); err != nil {
		return nil, fmt.Errorf("mkdir lock dir: %w", err)
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("acquire lock on %s: %w", lockPath, err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
