package openshell

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/safepath"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"golang.org/x/sys/unix"
)

// ActivationTarget comes from the current inspected container, after the
// caller verifies its launch/labels/binds against PreparedRuntime. It is read
// again before authorization; a saved inspect result is never the verifier.
type ActivationTarget struct {
	ContainerID string
	PID         int
	StartedAt   time.Time
}

type activationPeer struct {
	proc, pidfd int
	start       string
	identity    namespaceIdentity
	target      ActivationTarget
	prepared    PreparedRuntime
	inspect     func(context.Context) (ActivationTarget, error)
}

func (p *activationPeer) close() { _ = unix.Close(p.proc); _ = unix.Close(p.pidfd) }

func nativePreparedIdentity(prepared PreparedRuntime) error {
	c := spaceenv.Current()
	if c == nil || os.Geteuid() != 0 {
		return errors.New("activation requires the verified native root controller")
	}
	b := prepared.Record.Launch.Binding
	root, err := layout.OpenShellControlDir()
	if err != nil {
		return err
	}
	want := filepath.Join(root, "containers", b.Incarnation)
	if prepared.ContainerRoot != want || prepared.GenerationRoot != filepath.Join(want, "generations", prepared.Record.Generation) || b.Space != c.Space || b.DiskID != c.DiskID || b.DiskUUID != c.DiskUUID || b.ControllerGeneration != c.Generation {
		return errors.New("activation does not match the selected native disk/box/generation")
	}
	return nil
}

// pinActivationPeer uses SO_PEERCRED only to locate the candidate process.
// It independently pins procfs and a pidfd, verifies the exact helper argv,
// executable bytes and selected native record inode through that process's
// actual root, then matches its own namespace against the received FD.
func pinActivationPeer(ctx context.Context, conn *net.UnixConn, received *os.File, prepared PreparedRuntime, inspect func(context.Context) (ActivationTarget, error)) (_ *activationPeer, err error) {
	if err = nativePreparedIdentity(prepared); err != nil {
		return nil, err
	}
	if inspect == nil || received == nil {
		return nil, errors.New("activation lacks a live runtime verifier or namespace descriptor")
	}
	target, err := inspect(ctx)
	if err != nil {
		return nil, err
	}
	if !lowerHex(target.ContainerID, 32) || target.PID <= 1 || target.StartedAt.IsZero() {
		return nil, errors.New("activation lacks a complete live container identity")
	}
	credentials, err := activationCredentials(conn)
	if err != nil || credentials == nil || credentials.Uid != 0 || credentials.Gid != 0 || int(credentials.Pid) != target.PID {
		return nil, errors.New("activation peer is not the selected container's root PID 1")
	}
	pidfd, err := unix.PidfdOpen(target.PID, 0)
	if err != nil {
		return nil, err
	}
	p := &activationPeer{proc: -1, pidfd: pidfd, target: target, prepared: prepared, inspect: inspect}
	defer func() {
		if err != nil {
			p.close()
		}
	}()
	procRoot, err := unix.Open("/proc", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(procRoot) }()
	var fs unix.Statfs_t
	if err = unix.Fstatfs(procRoot, &fs); err != nil || fs.Type != unix.PROC_SUPER_MAGIC {
		return nil, errors.New("activation process lookup requires genuine procfs")
	}
	p.proc, err = unix.Openat(procRoot, strconv.Itoa(target.PID), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	if err = unix.PidfdSendSignal(p.pidfd, 0, nil, 0); err != nil {
		return nil, errors.New("activation process is no longer alive")
	}
	raw, err := readProcAt(p.proc, "stat", 64<<10)
	if err != nil {
		return nil, err
	}
	p.start, err = processStartIdentity(raw, target.PID)
	if err != nil {
		return nil, err
	}
	command, err := readProcAt(p.proc, "cmdline", 64<<10)
	if err != nil {
		return nil, err
	}
	expected, err := prepared.Record.Launch.HelperLaunch().Args()
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(command, []byte(strings.Join(expected, "\x00")+"\x00")) {
		return nil, errors.New("activation process is not the fixed selected helper launch")
	}
	// exe and root are intentional kernel procfs magic links reached through
	// the already pinned process directory, never another numeric PID lookup.
	exe, err := unix.Openat(p.proc, "exe", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(exe), "activation-peer-executable")
	hash := sha256.New()
	var st unix.Stat_t
	statErr := unix.Fstat(exe, &st)
	n, hashErr := io.Copy(hash, io.LimitReader(file, (256<<20)+1))
	_ = file.Close()
	if statErr != nil || hashErr != nil || n > 256<<20 || st.Uid != 0 || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&0o022 != 0 || hex.EncodeToString(hash.Sum(nil)) != prepared.Record.Launch.HelperSHA256 {
		return nil, errors.New("activation process does not execute the selected root-owned helper bytes")
	}
	if err = p.verifySelectedRecord(); err != nil {
		return nil, err
	}
	p.identity, err = networkNamespaceIdentity(int(received.Fd()))
	if err != nil {
		return nil, err
	}
	self, err := unix.Open("/proc/self/ns/net", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	selfIdentity, selfErr := networkNamespaceIdentity(self)
	_ = unix.Close(self)
	if selfErr != nil || selfIdentity == p.identity {
		return nil, errors.New("activation refuses the native controller/VM network namespace")
	}
	if err = p.revalidate(ctx); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *activationPeer) verifySelectedRecord() error {
	raw, info, err := safepath.ReadFileInfo(p.prepared.ContainerRoot, "current.json", 1<<20)
	if err != nil {
		return err
	}
	if err := privateFileOwner(info, 0, 0); err != nil {
		return err
	}
	want, err := json.Marshal(p.prepared.Record)
	if err != nil || !bytes.Equal(raw, want) {
		return errors.New("native selected runtime record changed during activation")
	}
	root, err := unix.Openat(p.proc, "root", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(root) }()
	// The process's actual projection must be the same native inode, not a
	// copy of an authentic record in another container or custom image.
	fd := root
	var opened []int
	defer func() {
		for _, item := range opened {
			_ = unix.Close(item)
		}
	}()
	for _, component := range []string{".openshell", "native"} {
		next, err := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return err
		}
		opened = append(opened, next)
		fd = next
	}
	record, err := unix.Openat(fd, "current.json", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(record) }()
	var actual unix.Stat_t
	expected, ok := info.Sys().(*syscall.Stat_t)
	if !ok || unix.Fstat(record, &actual) != nil || actual.Dev != uint64(expected.Dev) || actual.Ino != expected.Ino || actual.Uid != 0 || actual.Mode&0o7777 != 0o600 || actual.Nlink != 1 {
		return errors.New("activation peer root does not contain the selected native record bind")
	}
	return nil
}

func (p *activationPeer) revalidate(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := unix.PidfdSendSignal(p.pidfd, 0, nil, 0); err != nil {
		return errors.New("activation process exited before authorization")
	}
	raw, err := readProcAt(p.proc, "stat", 64<<10)
	if err != nil {
		return err
	}
	start, err := processStartIdentity(raw, p.target.PID)
	if err != nil || start != p.start {
		return errors.New("activation process identity changed")
	}
	current, err := p.inspect(ctx)
	if err != nil || current != p.target {
		return errors.New("activation container was stopped or replaced")
	}
	fd, err := unix.Openat(p.proc, "ns/net", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	identity, err := networkNamespaceIdentity(fd)
	_ = unix.Close(fd)
	if err != nil || identity != p.identity {
		return errors.New("received namespace is not the verified helper process's own network namespace")
	}
	return p.verifySelectedRecord()
}

func readProcAt(directory int, name string, limit int64) ([]byte, error) {
	fd, err := unix.Openat(directory, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "activation-process-"+name)
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, errors.New("activation process metadata exceeds bound")
	}
	return raw, nil
}

func processStartIdentity(raw []byte, pid int) (string, error) {
	text := string(raw)
	open := strings.Index(text, " (")
	close := strings.LastIndex(text, ") ")
	if open < 1 || close <= open || text[:open] != strconv.Itoa(pid) {
		return "", errors.New("invalid activation process stat identity")
	}
	fields := strings.Fields(text[close+2:])
	if len(fields) < 20 || fields[0] == "Z" || fields[0] == "X" {
		return "", errors.New("activation process is missing or dead")
	}
	start, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil || start == 0 {
		return "", errors.New("invalid activation process start time")
	}
	return strconv.FormatUint(start, 10), nil
}

func activationCredentials(conn *net.UnixConn) (*unix.Ucred, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return nil, err
	}
	var credentials *unix.Ucred
	var socketErr error
	if err = raw.Control(func(fd uintptr) {
		credentials, socketErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return nil, err
	}
	return credentials, socketErr
}

func networkNamespaceIdentity(fd int) (namespaceIdentity, error) {
	// Linux include/uapi/linux/nsfs.h: NS_GET_NSTYPE = _IO(0xb7,3).
	kind, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), 0xb703, 0)
	if errno != 0 || kind != unix.CLONE_NEWNET {
		return namespaceIdentity{}, errors.New("activation descriptor is not a network namespace")
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return namespaceIdentity{}, err
	}
	if st.Ino == 0 {
		return namespaceIdentity{}, errors.New("activation namespace has no inode identity")
	}
	return namespaceIdentity{uint64(st.Dev), st.Ino}, nil
}
