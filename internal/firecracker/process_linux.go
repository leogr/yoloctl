//go:build linux && amd64

package firecracker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/leogr/yoloctl/internal/safepath"
	"golang.org/x/sys/unix"
)

type process struct {
	PID   int    `json:"pid"`
	Start string `json:"start"`
}

const bootSettlementWait = 2 * time.Minute

// Signal only the child started by this invocation, even before publication.
func retireBootHelper(done <-chan error, signal func(os.Signal) error, kill func() error) error {
	_ = signal(unix.SIGTERM)
	grace := time.NewTimer(5 * time.Second)
	defer grace.Stop()
	select {
	case <-done:
		return nil
	case <-grace.C:
		_ = kill()
	}
	timer := time.NewTimer(bootSettlementWait)
	defer timer.Stop()
	select {
	case <-done:
		return nil
	case <-timer.C:
		return errors.New("VM helper termination could not be established")
	}
}

type runtimeState struct {
	Helper process `json:"helper"`
	VM     process `json:"vm"`
	Dir    string  `json:"dir"`
	// A crash between exec and publishing the child's identity must never be
	// mistaken for proof that it is safe to delete the native disk.
	LaunchPending bool `json:"launch_pending,omitempty"`
}

func identity(pid int) (process, error) {
	b, e := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if e != nil {
		return process{}, e
	}
	i := strings.LastIndex(string(b), ") ")
	if i < 0 {
		return process{}, errors.New("invalid process identity")
	}
	tail := string(b[i+2:])
	fields := strings.Fields(tail)
	if len(fields) < 20 {
		return process{}, errors.New("short process identity")
	}
	if fields[0] == "Z" {
		return process{}, os.ErrNotExist
	}
	return process{pid, fields[19]}, nil
}
func (p process) alive() (bool, error) {
	if p.PID == 0 {
		return false, nil
	}
	current, e := identity(p.PID)
	if errors.Is(e, os.ErrNotExist) {
		return false, nil
	}
	return e == nil && current == p, e
}
func (p process) signal(sig unix.Signal) error {
	if p.PID <= 1 || p.Start == "" {
		return errors.New("missing process identity")
	}
	fd, e := unix.PidfdOpen(p.PID, 0)
	if errors.Is(e, unix.ESRCH) {
		return nil
	}
	if e != nil {
		return e
	}
	defer func() { _ = unix.Close(fd) }()
	current, e := identity(p.PID)
	if errors.Is(e, os.ErrNotExist) {
		return nil
	}
	if e != nil {
		return e
	}
	if current != p {
		return nil
	}
	return unix.PidfdSendSignal(fd, sig, nil, 0)
}
func readState(dir string) (runtimeState, error) {
	var s runtimeState
	b, e := safepath.ReadFile(dir, "runtime.json", 8192)
	if e != nil {
		return s, e
	}
	e = strictJSON(b, &s)
	valid := func(p process, optional bool) bool {
		if optional && p.PID == 0 && p.Start == "" {
			return true
		}
		_, err := strconv.ParseUint(p.Start, 10, 64)
		return p.PID >= 2 && err == nil
	}
	if e == nil && (!valid(s.Helper, false) || !valid(s.VM, true) || !filepath.IsAbs(s.Dir) || filepath.Clean(s.Dir) != s.Dir || !strings.HasPrefix(filepath.Base(s.Dir), "yfc-")) {
		e = errors.New("invalid VM process record")
	}
	return s, e
}
func writeState(dir string, s runtimeState) error {
	b, e := json.Marshal(s)
	if e != nil {
		return e
	}
	return safepath.WriteFile(dir, "runtime.json", b, 0600)
}
func running(dir string) (bool, error) {
	s, e := readState(dir)
	if errors.Is(e, os.ErrNotExist) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	a, e := s.Helper.alive()
	if e != nil {
		return false, e
	}
	b, e := s.VM.alive()
	if e == nil && !a && s.LaunchPending {
		return false, errors.New("VM launch interrupted; termination is not established; retain its disks until termination is independently established")
	}
	return a || b, e
}
func lockFile(path string) (*os.File, error) {
	fd, e := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if e != nil {
		return nil, e
	}
	f := os.NewFile(uintptr(fd), path)
	if e = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); e != nil {
		_ = f.Close()
		return nil, fmt.Errorf("VM or disk is in use: %w", e)
	}
	return f, nil
}

// Only this helper's published record can establish death of its VMM. Missing
// prepublication state and a prior boot's record are not evidence of death.
func bootVMAlive(dir string, owner process, published *bool) error {
	s, err := readState(dir)
	if errors.Is(err, os.ErrNotExist) && !*published {
		return nil
	}
	if err != nil {
		return fmt.Errorf("observe boot runtime: %w", err)
	}
	if s.Helper != owner || owner.Start == "" {
		if *published {
			return errors.New("boot runtime ownership changed")
		}
		return nil
	}
	*published = true
	if s.VM.PID == 0 {
		return nil
	}
	live, err := s.VM.alive()
	if err != nil {
		return fmt.Errorf("observe boot VMM: %w", err)
	}
	if !live {
		return errors.New("VM process exited before readiness")
	}
	return nil
}

// settleBootFailure is shared by idle/cancellation/observation failure. Before
// publication an old record is unrelated to this Start's exact child: settle
// that child, then only stop a runtime it actually published. After publication
// losing ownership is uncertain, so retain it rather than signal a peer.
func settleBootFailure(ctx context.Context, dir string, owner process, published bool, terminate func() error, stop func(context.Context) error) error {
	// The caller shields settlement from cancellation. Share this budget across
	// both status reads so a silent helper cannot keep settlement running forever.
	storage, cancelStorage := context.WithTimeout(ctx, bootIdleTimeout)
	defer cancelStorage()
	s, err := readState(dir)
	if err != nil && (!errors.Is(err, os.ErrNotExist) || published) {
		return err
	}
	if err == nil {
		if s.Helper == owner {
			if err := checkHostStoragePause(storage, s); err != nil {
				return err
			}
			published = true
		} else if published {
			return errors.New("boot runtime ownership changed; retaining runtime")
		}
	}
	if err := terminate(); err != nil {
		return err
	}
	// Preparation could publish between the first observation and termination.
	// Re-read after exact child settlement instead of ever stopping the old record.
	s, err = readState(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if s.Helper != owner {
		if published {
			return errors.New("boot runtime ownership changed after helper settlement")
		}
		return nil
	}
	if err := checkHostStoragePause(storage, s); err != nil {
		return err
	}
	cleanup, cancel := context.WithTimeout(ctx, bootSettlementWait)
	defer cancel()
	return stop(cleanup)
}
