package agentbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"

	"github.com/leogr/yoloctl/internal/port"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"github.com/leogr/yoloctl/internal/vmwire"
	"golang.org/x/sys/unix"
)

type forwardProcessIdentity struct {
	PID        int
	StartTicks string
}

func currentForwardProcessIdentity() (forwardProcessIdentity, error) {
	return observedForwardProcessIdentity(os.Getpid())
}

func observedForwardProcessIdentity(pid int) (forwardProcessIdentity, error) {
	if pid <= 1 {
		return forwardProcessIdentity{}, errors.New("invalid observed forward process PID")
	}
	root, err := unix.Open("/proc", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return forwardProcessIdentity{}, err
	}
	defer func() { _ = unix.Close(root) }()
	var filesystem unix.Statfs_t
	if unix.Fstatfs(root, &filesystem) != nil || filesystem.Type != unix.PROC_SUPER_MAGIC {
		return forwardProcessIdentity{}, errors.New("forward process identity requires genuine procfs")
	}
	process, err := unix.Openat(root, strconv.Itoa(pid), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return forwardProcessIdentity{}, err
	}
	defer func() { _ = unix.Close(process) }()
	raw, err := readForwardProcAt(process, "stat")
	if err != nil {
		return forwardProcessIdentity{}, err
	}
	start, err := forwardProcessStart(raw, pid)
	return forwardProcessIdentity{pid, start}, err
}

// pinnedForwardProcess grants no signal/adoption authority. It holds kernel
// identities only for a current creation/readiness observation.
type pinnedForwardProcess struct {
	process, pidfd int
	identity       forwardProcessIdentity
	argv           []byte
	executable     os.FileInfo
	uid, gid       uint32
}

func (p *pinnedForwardProcess) Close() error {
	return errors.Join(unix.Close(p.process), unix.Close(p.pidfd))
}

// pinNativeForwardProcess is for a NEW admitted creation, not adoption of an
// old unit after controller replacement. The fixed worker must execute this
// selected controller's actual inode; dynamic VM CLIs remain supported.
func pinNativeForwardProcess(ctx context.Context, unit vmwire.UnitProcess, identity forwardProcessIdentity, spec port.Spec, binding port.ForwardBinding) (*pinnedForwardProcess, error) {
	if !spaceenv.Native() || os.Geteuid() != 0 || os.Getegid() != 0 {
		return nil, errors.New("forward process proof requires native VM root")
	}
	name, err := port.OpenShellUnitName(spec)
	if err != nil {
		return nil, err
	}
	if unit.Name != name || unit.State != "active" || unit.PID != identity.PID {
		return nil, errors.New("forward status is not the current unit main process")
	}
	args, err := port.ForwardWorkerArgs(spec, binding)
	if err != nil {
		return nil, err
	}
	// Both exe paths here are intentional kernel aliases, not caller paths.
	selected, err := os.Open("/proc/self/exe")
	if err != nil {
		return nil, err
	}
	defer func() { _ = selected.Close() }()
	info, err := selected.Stat()
	if err != nil {
		return nil, err
	}
	return pinForwardProcess(ctx, identity, append([]string{vmwire.GuestBinary}, args...), info, 0, 0)
}

// pinForwardProcess is a read-only procfs verifier. Production supplies only
// fixed native inputs above; tests observe their ordinary local process.
func pinForwardProcess(ctx context.Context, identity forwardProcessIdentity, argv []string, executable os.FileInfo, uid, gid uint32) (_ *pinnedForwardProcess, err error) {
	start, parseErr := strconv.ParseUint(identity.StartTicks, 10, 64)
	if identity.PID <= 1 || parseErr != nil || start == 0 || strconv.FormatUint(start, 10) != identity.StartTicks || executable == nil || len(argv) == 0 {
		return nil, errors.New("incomplete forward process identity")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	pidfd, err := unix.PidfdOpen(identity.PID, 0)
	if err != nil {
		return nil, fmt.Errorf("pin forward worker pidfd: %w", err)
	}
	p := &pinnedForwardProcess{process: -1, pidfd: pidfd, identity: identity, argv: []byte(strings.Join(argv, "\x00") + "\x00"), executable: executable, uid: uid, gid: gid}
	defer func() {
		if err != nil {
			_ = p.Close()
		}
	}()
	root, err := unix.Open("/proc", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(root) }()
	var filesystem unix.Statfs_t
	if unix.Fstatfs(root, &filesystem) != nil || filesystem.Type != unix.PROC_SUPER_MAGIC {
		return nil, errors.New("forward process lookup requires genuine procfs")
	}
	p.process, err = unix.Openat(root, strconv.Itoa(identity.PID), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	if err = p.verify(ctx); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *pinnedForwardProcess) alive() error {
	descriptors := []unix.PollFd{{Fd: int32(p.pidfd), Events: unix.POLLIN}}
	if _, err := unix.Poll(descriptors, 0); err != nil {
		return err
	}
	if descriptors[0].Revents != 0 {
		return errors.New("forward worker has exited")
	}
	return nil
}

func (p *pinnedForwardProcess) verify(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := p.alive(); err != nil {
		return err
	}
	stat, err := readForwardProcAt(p.process, "stat")
	if err != nil {
		return err
	}
	start, err := forwardProcessStart(stat, p.identity.PID)
	if err != nil || start != p.identity.StartTicks {
		return errors.New("forward worker process start changed")
	}
	status, err := readForwardProcAt(p.process, "status")
	if err != nil {
		return err
	}
	if err := verifyForwardProcessOwners(status, p.uid, p.gid); err != nil {
		return err
	}
	command, err := readForwardProcAt(p.process, "cmdline")
	if err != nil {
		return err
	}
	if !bytes.Equal(command, p.argv) {
		return errors.New("forward worker is not the fixed declared role/attempt")
	}
	fd, err := unix.Openat(p.process, "exe", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), "forward-worker-executable")
	info, statErr := file.Stat()
	closeErr := file.Close()
	if statErr != nil || closeErr != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 || !os.SameFile(info, p.executable) {
		return errors.New("forward worker does not execute the selected controller inode")
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != p.uid {
		return errors.New("forward executable is not owned by its trusted infrastructure user")
	}
	// The listener belongs in the VM's own network namespace. These aliases
	// are reached only through genuine procfs and the pinned process directory.
	actual, err := unix.Openat(p.process, "ns/net", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(actual) }()
	selected, err := unix.Open("/proc/self/ns/net", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(selected) }()
	var want, got unix.Stat_t
	if unix.Fstat(actual, &got) != nil || unix.Fstat(selected, &want) != nil || got.Dev != want.Dev || got.Ino != want.Ino {
		return errors.New("forward worker is outside the selected VM network namespace")
	}
	return p.alive()
}

func readForwardProcAt(directory int, name string) ([]byte, error) {
	fd, err := unix.Openat(directory, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "forward-process-"+name)
	defer func() { _ = file.Close() }()
	raw, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > 64<<10 {
		return nil, errors.New("forward process metadata exceeds bound")
	}
	return raw, nil
}

func forwardProcessStart(raw []byte, pid int) (string, error) {
	text := string(raw)
	open, close := strings.Index(text, " ("), strings.LastIndex(text, ") ")
	if open < 1 || close <= open || text[:open] != strconv.Itoa(pid) {
		return "", errors.New("invalid forward process stat identity")
	}
	fields := strings.Fields(text[close+2:])
	if len(fields) < 20 || fields[0] == "Z" || fields[0] == "X" || fields[0] == "x" {
		return "", errors.New("forward process is missing or dead")
	}
	start, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil || start == 0 {
		return "", errors.New("invalid forward process start ticks")
	}
	return strconv.FormatUint(start, 10), nil
}

func verifyForwardProcessOwners(raw []byte, uid, gid uint32) error {
	seen := make(map[string]bool)
	for _, line := range strings.Split(string(raw), "\n") {
		key, values, _ := strings.Cut(line, ":")
		if key != "Uid" && key != "Gid" {
			continue
		}
		if seen[key] {
			return errors.New("duplicate process ownership field")
		}
		seen[key] = true
		fields := strings.Fields(values)
		if len(fields) != 4 {
			return errors.New("incomplete process ownership field")
		}
		want := uid
		if key == "Gid" {
			want = gid
		}
		for _, value := range fields {
			if value != strconv.FormatUint(uint64(want), 10) {
				return fmt.Errorf("forward process %s changed", key)
			}
		}
	}
	if len(seen) != 2 {
		return errors.New("missing process ownership fields")
	}
	return nil
}
