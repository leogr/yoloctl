//go:build linux && amd64

package firecracker

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/leogr/yoloctl/internal/safepath"
	"golang.org/x/sys/unix"
)

type process struct {
	PID   int    `json:"pid"`
	Start string `json:"start"`
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
		return false, errors.New("VM launch outcome is uncertain; retain its disks until termination is independently established")
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
