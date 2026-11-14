package openshell

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"
)

const runtimeMainExitName = "main-exit"

// runtimeMainExit is carried through normal relay/child cleanup to the helper's
// entry point. A nil code means a present but invalid marker, not a known exit.
type runtimeMainExit struct{ code *int }

func (e *runtimeMainExit) Error() string {
	if e.code == nil {
		return "OpenShell main exited without a valid exit code"
	}
	return fmt.Sprintf("OpenShell main exited with code %d", *e.code)
}

func (e *runtimeMainExit) exitCode() int {
	if e.code == nil {
		return 1
	}
	return *e.code
}

// A failed observation remains unavailable. A no-follow type/owner observation
// establishes an invalid marker; valid data additionally requires opening and
// inspecting that descriptor. O_NONBLOCK never waits on a substituted FIFO.
func readRuntimeMainExitAt(dir *os.File, uid, gid int) (*runtimeMainExit, error) {
	// Inspect without following links first: a socket/device can refuse open,
	// but its observed wrong type still proves an invalid, present marker.
	var named unix.Stat_t
	if err := unix.Fstatat(int(dir.Fd()), runtimeMainExitName, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil, nil
		}
		return nil, err
	}
	if named.Mode&unix.S_IFMT != unix.S_IFREG || named.Uid != uint32(uid) || named.Gid != uint32(gid) {
		return &runtimeMainExit{}, nil
	}
	fd, err := unix.Openat(int(dir.Fd()), runtimeMainExitName, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if errors.Is(err, unix.ELOOP) {
		return &runtimeMainExit{}, nil
	}
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), runtimeMainExitName)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Uid != uint32(uid) || stat.Gid != uint32(gid) {
		if err := file.Close(); err != nil {
			return nil, err
		}
		return &runtimeMainExit{}, nil
	}
	// The complete canonical marker is at most len("exit_code=255\n").
	raw, err := io.ReadAll(io.LimitReader(file, int64(len("exit_code=255\n")+1)))
	err = errors.Join(err, file.Close())
	if err != nil {
		return nil, err
	}
	code, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(string(raw), "exit_code="), "\n"))
	if err != nil || code < 0 || code > 255 || string(raw) != fmt.Sprintf("exit_code=%d\n", code) {
		return &runtimeMainExit{}, nil
	}
	return &runtimeMainExit{code: &code}, nil
}

func clearRuntimeMainExitAt(dir *os.File, uid, gid int) error {
	var stat unix.Stat_t
	if err := unix.Fstatat(int(dir.Fd()), runtimeMainExitName, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		return err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Uid != uint32(uid) || stat.Gid != uint32(gid) {
		return errors.New("stale main-exit marker has invalid type or ownership")
	}
	if err := unix.Unlinkat(int(dir.Fd()), runtimeMainExitName, 0); err != nil {
		return err
	}
	return dir.Sync()
}

func stopForRuntimeMainExit(record RuntimeRecord, children []*runtimeProcess, exit *runtimeMainExit, admission *atomic.Bool, publish func(RuntimeRecord, RuntimeStatus) error) error {
	admission.Store(false)
	status := observeRuntimeEnd(record, "stopping", "cleanup-unproved", children, exit, time.Now())
	if err := publish(record, status); err != nil {
		return errors.Join(exit, err)
	}
	return exit
}

func observeRuntimeEnd(record RuntimeRecord, phase, reason string, children []*runtimeProcess, outcome error, now time.Time) RuntimeStatus {
	status := observeRuntimeStatus(record, phase, reason, children, now)
	var exit *runtimeMainExit
	if errors.As(outcome, &exit) {
		status.Reason, status.MainExit = "main-exited", exit.code
	}
	return status
}

// The pinned supervisor publishes through .main-exit.tmp-<its PID>, mode 0600.
func runtimeMainExitTemporary(name string) bool {
	const prefix = ".main-exit.tmp-"
	if !strings.HasPrefix(name, prefix) {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimPrefix(name, prefix))
	return err == nil && pid > 0 && name == prefix+strconv.Itoa(pid)
}

// Normal cleanup uses errors.Join. Report its failures without treating the
// main process's ordinary exit as an infrastructure failure.
func runtimeMainExitDiagnostics(err error) error {
	if _, ok := err.(*runtimeMainExit); ok {
		return nil
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		var diagnostics error
		for _, part := range joined.Unwrap() {
			diagnostics = errors.Join(diagnostics, runtimeMainExitDiagnostics(part))
		}
		return diagnostics
	}
	return err
}
