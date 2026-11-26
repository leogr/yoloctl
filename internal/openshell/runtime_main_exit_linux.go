package openshell

import (
	"bytes"
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

const (
	runtimeMainExitName             = "main-exit"
	runtimeMainAttachmentsEndedName = "main-attachments-ended"
	// A fixed wait whose end fails nothing: the box stops as it should once
	// its main process has exited. It is kept independent of the endpoint's
	// close fail-safe, which would keep a finished box up for minutes.
	runtimeMainExitWait = 4 * time.Second
)

// The second marker only proves the existing attachment wait ended. Detach,
// transport loss and normal completion all count; it is not an output receipt.
func readRuntimeMainAttachmentsEndedAt(dir *os.File, uid, gid int) (bool, error) {
	raw, present, err := readRuntimeMainMarkerAt(dir, runtimeMainAttachmentsEndedName, uid, gid, len("ended\n"))
	return present && bytes.Equal(raw, []byte("ended\n")), err
}

// The clock is supplied by the monitor; tests advance it without sleeping.
// This single overall budget does not observe session.finish's phase boundary.
type runtimeMainExitDrain struct {
	exit     *runtimeMainExit
	deadline time.Time
	result   error
}

func (d runtimeMainExitDrain) complete(now time.Time, attachmentsEnded bool) bool {
	return d.exit.code == nil || attachmentsEnded || !now.Before(d.deadline)
}

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
	raw, present, err := readRuntimeMainMarkerAt(dir, runtimeMainExitName, uid, gid, len("exit_code=255\n"))
	if err != nil || !present {
		return nil, err
	}
	code, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(string(raw), "exit_code="), "\n"))
	if err != nil || code < 0 || code > 255 || string(raw) != fmt.Sprintf("exit_code=%d\n", code) {
		return &runtimeMainExit{}, nil
	}
	return &runtimeMainExit{code: &code}, nil
}

func readRuntimeMainMarkerAt(dir *os.File, name string, uid, gid, maximum int) ([]byte, bool, error) {
	// Inspect without following links first: a socket/device can refuse open,
	// but its observed wrong type still proves an invalid, present marker.
	var named unix.Stat_t
	if err := unix.Fstatat(int(dir.Fd()), name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if named.Mode&unix.S_IFMT != unix.S_IFREG || named.Uid != uint32(uid) || named.Gid != uint32(gid) {
		return nil, true, nil
	}
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if errors.Is(err, unix.ELOOP) {
		return nil, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	file := os.NewFile(uintptr(fd), name)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return nil, false, errors.Join(err, file.Close())
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Uid != uint32(uid) || stat.Gid != uint32(gid) {
		if err := file.Close(); err != nil {
			return nil, false, err
		}
		return nil, true, nil
	}
	// Read one extra byte to reject noncanonical/trailing content.
	raw, err := io.ReadAll(io.LimitReader(file, int64(maximum+1)))
	err = errors.Join(err, file.Close())
	if err != nil {
		return nil, false, err
	}
	return raw, true, nil
}

func clearRuntimeMainExitAt(dir *os.File, uid, gid int) error {
	for _, name := range []string{runtimeMainExitName, runtimeMainAttachmentsEndedName} {
		var stat unix.Stat_t
		if err := unix.Fstatat(int(dir.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			if errors.Is(err, unix.ENOENT) {
				continue
			}
			return err
		}
		if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Uid != uint32(uid) || stat.Gid != uint32(gid) {
			return fmt.Errorf("stale %s marker has invalid type or ownership", name)
		}
		if err := unix.Unlinkat(int(dir.Fd()), name, 0); err != nil {
			return err
		}
		if err := dir.Sync(); err != nil {
			return err
		}
	}
	return nil
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

// Both markers use the pinned atomic producer's .<basename>.tmp-<PID>.
func runtimeMainExitTemporary(name string) bool {
	for _, marker := range []string{runtimeMainExitName, runtimeMainAttachmentsEndedName} {
		prefix := "." + marker + ".tmp-"
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimPrefix(name, prefix))
		return err == nil && pid > 0 && name == prefix+strconv.Itoa(pid)
	}
	return false
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
