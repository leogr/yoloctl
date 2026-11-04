//go:build linux

// Package vmanchor carries original VM-launch evidence. It does not authorize
// cleanup. Its Firecracker source is initialized only by the guest boot path;
// the consumer must verify continuity of that exact launch before using it.
package vmanchor

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const launchFrameLimit = 8 << 10

type fileIdentity struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}

type processIdentity struct {
	PID       int          `json:"pid"`
	Parent    int          `json:"parent"`
	Start     uint64       `json:"start"`
	Directory fileIdentity `json:"directory"`
	Namespace fileIdentity `json:"namespace"`
}

type launchRecord struct {
	Version  int             `json:"version"`
	Launch   string          `json:"launch"`
	Boot     string          `json:"boot"`
	Launcher processIdentity `json:"launcher"`
	Child    processIdentity `json:"child"`
}

type processPin struct {
	identity                    processIdentity
	directory, namespace, pidfd *os.File
}

func closeFiles(files ...*os.File) error {
	var err error
	for _, file := range files {
		if file != nil {
			err = errors.Join(err, file.Close())
		}
	}
	return err
}

func (p *processPin) close() error {
	if p == nil {
		return nil
	}
	return closeFiles(p.directory, p.namespace, p.pidfd)
}

func openAt(parent *os.File, path string, flags int) (*os.File, error) {
	fd, err := unix.Openat(int(parent.Fd()), path, flags|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

func identity(file *os.File) (fileIdentity, error) {
	if file == nil {
		return fileIdentity{}, errors.New("closed launch object")
	}
	var st unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &st); err != nil {
		return fileIdentity{}, err
	}
	if st.Dev == 0 || st.Ino == 0 {
		return fileIdentity{}, errors.New("missing launch object identity")
	}
	return fileIdentity{uint64(st.Dev), st.Ino}, nil
}

func procDirectory(file *os.File) error {
	var fs unix.Statfs_t
	var st unix.Stat_t
	if file == nil || unix.Fstatfs(int(file.Fd()), &fs) != nil || fs.Type != unix.PROC_SUPER_MAGIC || unix.Fstat(int(file.Fd()), &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFDIR {
		return errors.New("launch process directory is not procfs")
	}
	return nil
}

func pidNamespace(file *os.File) (fileIdentity, error) {
	if file == nil {
		return fileIdentity{}, errors.New("missing launch namespace")
	}
	var fs unix.Statfs_t
	kind, _, errno := unix.Syscall(unix.SYS_IOCTL, file.Fd(), 0xb703, 0) // NS_GET_NSTYPE
	if errno != 0 || kind != unix.CLONE_NEWPID || unix.Fstatfs(int(file.Fd()), &fs) != nil || fs.Type != unix.NSFS_MAGIC {
		return fileIdentity{}, errors.New("launch namespace is not a typed PID namespace")
	}
	return identity(file)
}

func readAt(parent *os.File, path string, limit int64) (raw []byte, err error) {
	f, err := openAt(parent, path, unix.O_RDONLY)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	raw, err = io.ReadAll(io.LimitReader(f, limit+1))
	if err == nil && int64(len(raw)) > limit {
		err = errors.New("launch metadata exceeds limit")
	}
	return raw, err
}

func processStat(directory *os.File) (pid, parent int, start uint64, err error) {
	raw, err := readAt(directory, "stat", 64<<10)
	if err != nil {
		return 0, 0, 0, err
	}
	left, right := strings.IndexByte(string(raw), '('), strings.LastIndexByte(string(raw), ')')
	if left < 1 || right <= left {
		return 0, 0, 0, errors.New("invalid launch process stat")
	}
	fields := strings.Fields(string(raw[right+1:]))
	if len(fields) < 20 {
		return 0, 0, 0, errors.New("short launch process stat")
	}
	pid, err = strconv.Atoi(strings.TrimSpace(string(raw[:left])))
	if err != nil {
		return 0, 0, 0, err
	}
	parent, err = strconv.Atoi(fields[1])
	if err != nil {
		return 0, 0, 0, err
	}
	start, err = strconv.ParseUint(fields[19], 10, 64)
	if err == nil && (pid < 1 || parent < 0 || start == 0) {
		err = errors.New("invalid launch process incarnation")
	}
	return pid, parent, start, err
}

func pinProcess(root *os.File, pid int) (p *processPin, err error) {
	if pid < 1 {
		return nil, errors.New("invalid launch PID")
	}
	p = &processPin{}
	defer func() {
		if err != nil {
			err = errors.Join(err, p.close())
			p = nil
		}
	}()
	p.directory, err = openAt(root, strconv.Itoa(pid), unix.O_RDONLY|unix.O_DIRECTORY)
	if err != nil {
		return p, err
	}
	if err = procDirectory(p.directory); err != nil {
		return p, err
	}
	p.identity.Directory, err = identity(p.directory)
	if err != nil {
		return p, err
	}
	p.identity.PID, p.identity.Parent, p.identity.Start, err = processStat(p.directory)
	if err != nil {
		return p, err
	}
	if p.identity.PID != pid {
		return p, errors.New("launch proc view disagrees with PID")
	}
	nsdir, err := openAt(p.directory, "ns", unix.O_RDONLY|unix.O_DIRECTORY)
	if err != nil {
		return p, err
	}
	// Only this kernel proc magic link is followed, through the retained proc
	// process directory. Arbitrary caller-selected paths are never accepted.
	nsfd, nsErr := unix.Openat(int(nsdir.Fd()), "pid", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	closeErr := nsdir.Close()
	if nsErr != nil {
		return p, errors.Join(nsErr, closeErr)
	}
	p.namespace = os.NewFile(uintptr(nsfd), "launch-pid-namespace")
	if closeErr != nil {
		return p, closeErr
	}
	p.identity.Namespace, err = pidNamespace(p.namespace)
	if err != nil {
		return p, err
	}
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return p, err
	}
	p.pidfd = os.NewFile(uintptr(fd), "launch-original-pidfd")
	err = p.check()
	return p, err
}

func (p *processPin) check() error {
	if p == nil || p.pidfd == nil {
		return errors.New("missing original launch process")
	}
	poll := []unix.PollFd{{Fd: int32(p.pidfd.Fd()), Events: unix.POLLIN}}
	if _, err := unix.Poll(poll, 0); err != nil {
		return err
	}
	if poll[0].Revents != 0 {
		return errors.New("original launch process is no longer live")
	}
	if err := procDirectory(p.directory); err != nil {
		return err
	}
	id, err := identity(p.directory)
	if err != nil || id != p.identity.Directory {
		return errors.Join(errors.New("launch process directory changed"), err)
	}
	pid, parent, start, err := processStat(p.directory)
	if err != nil || pid != p.identity.PID || parent != p.identity.Parent || start != p.identity.Start {
		return errors.Join(errors.New("launch process incarnation changed"), err)
	}
	id, err = pidNamespace(p.namespace)
	if err != nil || id != p.identity.Namespace {
		return errors.Join(errors.New("retained launch namespace changed"), err)
	}
	// A retained namespace FD alone does not prove that its process is still
	// in that namespace. Reopen only the original proc directory's magic link.
	fd, err := unix.Openat(int(p.directory.Fd()), "ns/pid", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	current := os.NewFile(uintptr(fd), "launch-current-pid-namespace")
	currentID, idErr := pidNamespace(current)
	closeErr := current.Close()
	if idErr != nil || closeErr != nil || currentID != p.identity.Namespace {
		return errors.Join(errors.New("launch process left original namespace"), idErr, closeErr)
	}
	return nil
}

func bootIdentity(root *os.File) (string, error) {
	raw, err := readAt(root, "sys/kernel/random/boot_id", 128)
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(raw))
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return "", errors.New("invalid launch boot identity")
	}
	if _, err := hex.DecodeString(strings.ReplaceAll(value, "-", "")); err != nil {
		return "", err
	}
	return value, nil
}

func duplicate(file *os.File) (*os.File, error) {
	fd, err := unix.FcntlInt(file.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), file.Name()), nil
}

// FirecrackerSource is owned by original guest boot, survives controller
// restarts, and is never reconstructed from received launch metadata.
type FirecrackerSource struct {
	mu       sync.Mutex
	root     *os.File
	launcher *processPin
	boot     string
	closed   bool
}

// CaptureFirecrackerSource is called only from validated guest boot, before
// the control listener is exposed. PID1 is a guard, not its provenance proof:
// the reviewed actual boot call graph supplies that trust premise.
func CaptureFirecrackerSource() (*FirecrackerSource, error) {
	if os.Getpid() != 1 || os.Geteuid() != 0 {
		return nil, errors.New("launch source requires original guest boot")
	}
	return captureSource()
}

func captureSource() (s *FirecrackerSource, err error) {
	s = &FirecrackerSource{}
	defer func() {
		if err != nil {
			err = errors.Join(err, s.Close())
			s = nil
		}
	}()
	fd, err := unix.Open("/proc", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return s, err
	}
	s.root = os.NewFile(uintptr(fd), "launch-proc-root")
	if err = procDirectory(s.root); err != nil {
		return s, err
	}
	s.boot, err = bootIdentity(s.root)
	if err != nil {
		return s, err
	}
	s.launcher, err = pinProcess(s.root, os.Getpid())
	if err != nil {
		return s, err
	}
	// Authenticate that the numeric process directory is this actual caller.
	selfFD, err := unix.Openat(int(s.root.Fd()), "self", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return s, err
	}
	self := os.NewFile(uintptr(selfFD), "launch-proc-self")
	id, idErr := identity(self)
	closeErr := self.Close()
	if idErr != nil || closeErr != nil || id != s.launcher.identity.Directory {
		return s, errors.Join(errors.New("launch proc view is not caller view"), idErr, closeErr)
	}
	return s, nil
}

func (s *FirecrackerSource) check() error {
	if s == nil || s.closed || s.root == nil {
		return errors.New("launch source unavailable")
	}
	boot, err := bootIdentity(s.root)
	if err != nil || boot != s.boot {
		return errors.Join(errors.New("original launch boot changed"), err)
	}
	if os.Getpid() != s.launcher.identity.PID {
		return errors.New("launch source used by another process")
	}
	return s.launcher.check()
}

func (s *FirecrackerSource) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return errors.Join(closeFiles(s.root), s.launcher.close())
}

// LaunchTransfer belongs to one Start attempt; it cannot publish twice. Close
// closes only its own descriptors and never touches or waits for the child.
type LaunchTransfer struct {
	mu                       sync.Mutex
	source                   *FirecrackerSource
	namespace, child, parent *os.File
	launch                   string
	attempted, closed        bool
}

func (s *FirecrackerSource) PrepareLaunch() (t *LaunchTransfer, err error) {
	if s == nil {
		return nil, errors.New("launch source unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err = s.check(); err != nil {
		return nil, err
	}
	t = &LaunchTransfer{source: s}
	defer func() {
		if err != nil {
			err = errors.Join(err, t.Close())
			t = nil
		}
	}()
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return t, err
	}
	t.launch = hex.EncodeToString(nonce[:])
	t.namespace, err = duplicate(s.launcher.namespace)
	if err != nil {
		return t, err
	}
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, 0)
	if err != nil {
		return t, err
	}
	t.parent, t.child = os.NewFile(uintptr(pair[0]), "launch-parent"), os.NewFile(uintptr(pair[1]), "launch-child")
	// Enable kernel packet credentials before publication, not after a child
	// races the parent's single nonblocking send.
	err = unix.SetsockoptInt(pair[1], unix.SOL_SOCKET, unix.SO_PASSCRED, 1)
	return t, err
}

// ChildFiles are reserved as FD3(namespace)/FD4(seqpacket) only by the fixed
// controller serve exec. They remain owned by this transfer until Close.
func (t *LaunchTransfer) ChildFiles() []*os.File {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed || t.attempted {
		return nil
	}
	return []*os.File{t.namespace, t.child}
}

// PublishStarted is called after successful original Start, outside its reaper
// lock. Any error is proof unavailability; it must not rewrite Start or Wait.
func (t *LaunchTransfer) PublishStarted(child *os.Process) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed || t.attempted {
		return errors.New("launch transfer already finished")
	}
	t.attempted = true
	// Start has copied these descriptors into the child. Drop the parent's
	// copies before publication; retain only its private sending endpoint.
	closeErr := closeFiles(t.namespace, t.child)
	t.namespace, t.child = nil, nil
	if closeErr != nil {
		return closeErr
	}
	if child == nil {
		return errors.New("missing actual started child")
	}
	s := t.source
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(); err != nil {
		return err
	}
	pin, err := pinProcess(s.root, child.Pid)
	if err != nil {
		return err
	}
	defer func() { _ = pin.close() }()
	if pin.identity.Parent != s.launcher.identity.PID || pin.identity.Namespace != s.launcher.identity.Namespace {
		return errors.New("started child is outside original launch scope")
	}
	frame, err := json.Marshal(launchRecord{Version: 1, Launch: t.launch, Boot: s.boot, Launcher: s.launcher.identity, Child: pin.identity})
	if err != nil {
		return err
	}
	if len(frame) > launchFrameLimit {
		return errors.New("launch frame exceeds publication limit")
	}
	if err := pin.check(); err != nil {
		return err
	}
	if err := s.check(); err != nil {
		return err
	}
	n, err := unix.SendmsgN(int(t.parent.Fd()), frame, nil, nil, unix.MSG_DONTWAIT|unix.MSG_NOSIGNAL)
	if err != nil {
		return err
	}
	if n != len(frame) {
		return fmt.Errorf("short launch publication: %d/%d", n, len(frame))
	}
	return nil
}

func (t *LaunchTransfer) Close() error {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil
	}
	t.closed = true
	return closeFiles(t.namespace, t.child, t.parent)
}

// LaunchAnchor retains the original validated controller launch. It remains
// controller-owned; workers receive only a checked namespace duplicate and
// separately checked callback-scoped parent custody, never a serialized anchor.
type LaunchAnchor struct {
	mu              sync.Mutex
	root, namespace *os.File
	launcher, child *processPin
	boot, launch    string
	closed          bool
	failed          error
}

type inheritedPair struct {
	namespace, socket fileIdentity
	creator           unix.Ucred
}

// inspectPair never wraps, closes or waits on either caller descriptor. Both
// raw slots must be inherited, non-CLOEXEC objects before any allocation.
func inspectPair(namespaceFD, transportFD int, launcherPID int) (p inheritedPair, err error) {
	if namespaceFD < 3 || transportFD < 3 || namespaceFD == transportFD {
		return p, errors.New("invalid launch slots")
	}
	for _, fd := range []int{namespaceFD, transportFD} {
		flags, e := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
		if e != nil || flags&unix.FD_CLOEXEC != 0 {
			return p, errors.New("pair is not an inherited launch transfer")
		}
	}
	var nsfs unix.Statfs_t
	var nsstat, sockstat unix.Stat_t
	kind, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(namespaceFD), 0xb703, 0)
	if errno != 0 || kind != unix.CLONE_NEWPID || unix.Fstatfs(namespaceFD, &nsfs) != nil || nsfs.Type != unix.NSFS_MAGIC || unix.Fstat(namespaceFD, &nsstat) != nil {
		return p, errors.New("inherited namespace is unavailable")
	}
	if err := unix.Fstat(transportFD, &sockstat); err != nil || sockstat.Mode&unix.S_IFMT != unix.S_IFSOCK {
		return p, errors.New("inherited launch socket is unavailable")
	}
	domain, err := unix.GetsockoptInt(transportFD, unix.SOL_SOCKET, unix.SO_DOMAIN)
	if err != nil || domain != unix.AF_UNIX {
		return p, errors.New("wrong launch socket domain")
	}
	typ, err := unix.GetsockoptInt(transportFD, unix.SOL_SOCKET, unix.SO_TYPE)
	if err != nil || typ != unix.SOCK_SEQPACKET {
		return p, errors.New("wrong launch socket type")
	}
	passcred, err := unix.GetsockoptInt(transportFD, unix.SOL_SOCKET, unix.SO_PASSCRED)
	if err != nil || passcred != 1 {
		return p, errors.New("launch socket lacks original credential setup")
	}
	creator, err := unix.GetsockoptUcred(transportFD, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil || int(creator.Pid) != launcherPID || os.Getppid() != launcherPID || (launcherPID == 1 && creator.Uid != 0) {
		return p, errors.New("launch socket creator is not original parent")
	}
	p = inheritedPair{namespace: fileIdentity{uint64(nsstat.Dev), nsstat.Ino}, socket: fileIdentity{uint64(sockstat.Dev), sockstat.Ino}, creator: *creator}
	if p.namespace.Device == 0 || p.namespace.Inode == 0 || p.socket.Device == 0 || p.socket.Inode == 0 {
		return p, errors.New("missing inherited pair identity")
	}
	return p, nil
}

// ConsumeInheritedFirecrackerLaunch must run early in the fixed serve entry,
// before unrelated descriptor allocation. claimed=false means BOTH originals
// remain untouched; claimed=true transfers BOTH to this function, including on
// error. Do not pre-wrap the raw slots in os.File. Missing proof is unavailable,
// never empty-census success. The reviewed original boot call graph supplies
// provenance; this is not a hostile replacement-VM-root anti-forgery protocol.
func ConsumeInheritedFirecrackerLaunch(namespaceFD, transportFD int) (*LaunchAnchor, bool, error) {
	return consumeInheritedLaunch(namespaceFD, transportFD, 1, 5*time.Second)
}

func consumeInheritedLaunch(namespaceFD, transportFD, launcherPID int, budget time.Duration) (a *LaunchAnchor, claimed bool, err error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	// Registered before temporary cleanup: a temporary Close failure must
	// also reject/close the result, rather than return a live anchor+error.
	defer func() {
		if err != nil && a != nil {
			err = errors.Join(err, a.Close())
			a = nil
		}
	}()
	before, err := inspectPair(namespaceFD, transportFD, launcherPID)
	if err != nil {
		return nil, false, err
	}
	// Only now may we allocate: both original slots are present, so these
	// owned process/proc handles cannot accidentally occupy absent old slots.
	self, err := captureSource()
	if err != nil {
		return nil, false, err
	}
	defer func() { err = errors.Join(err, self.Close()) }()
	launcher, err := pinProcess(self.root, launcherPID)
	if err != nil {
		return nil, false, err
	}
	defer func() {
		if launcher != nil {
			err = errors.Join(err, launcher.close())
		}
	}()
	if launcher.identity.Namespace != before.namespace || self.launcher.identity.Namespace != before.namespace || self.launcher.identity.Parent != launcher.identity.PID {
		return nil, false, errors.New("launch pair is outside original parent/child namespace")
	}
	if err := launcher.check(); err != nil {
		return nil, false, err
	}
	if err := self.check(); err != nil {
		return nil, false, err
	}
	if err := checkActiveNamespace(self.root, before.namespace); err != nil {
		return nil, false, err
	}
	after, err := inspectPair(namespaceFD, transportFD, launcherPID)
	if err != nil || before != after {
		return nil, false, errors.Join(errors.New("launch pair changed before claim"), err)
	}
	// The pair is now positively owned. All subsequent failures close only
	// those two originals and our independently acquired validation handles.
	claimed = true
	a = &LaunchAnchor{launcher: launcher, boot: self.boot}
	launcher = nil
	// Track both originals before fcntl so even a partial CLOEXEC failure
	// closes both owned objects. No os.File wrapper precedes the claim.
	if _, err = unix.FcntlInt(uintptr(namespaceFD), unix.F_SETFD, unix.FD_CLOEXEC); err != nil {
		_ = unix.Close(namespaceFD)
		_ = unix.Close(transportFD)
		return a, true, err
	}
	if _, err = unix.FcntlInt(uintptr(transportFD), unix.F_SETFD, unix.FD_CLOEXEC); err != nil {
		_ = unix.Close(namespaceFD)
		_ = unix.Close(transportFD)
		return a, true, err
	}
	a.namespace = os.NewFile(uintptr(namespaceFD), "original-controller-pid-namespace")
	transport := os.NewFile(uintptr(transportFD), "original-controller-launch")
	defer func() { err = errors.Join(err, transport.Close()) }()
	a.root, err = duplicate(self.root)
	if err != nil {
		return a, true, err
	}
	a.child, err = cloneProcess(self.launcher)
	if err != nil {
		return a, true, err
	}
	frame, cred, err := receiveLaunchFrame(transportFD, time.Now().Add(budget))
	if err != nil {
		return a, true, err
	}
	if cred != before.creator {
		return a, true, errors.New("launch packet sender differs from original socket creator")
	}
	var record launchRecord
	if err = strictLaunchRecord(frame, &record); err != nil {
		return a, true, err
	}
	nonce, nonceErr := hex.DecodeString(record.Launch)
	if nonceErr != nil || len(nonce) != 16 || record.Version != 1 || record.Boot != a.boot || record.Launcher != a.launcher.identity || record.Child != a.child.identity {
		return a, true, errors.New("launch record does not bind original launcher and this child")
	}
	a.launch = record.Launch
	if err = a.check(); err != nil {
		return a, true, err
	}
	return a, true, nil
}

func checkActiveNamespace(root *os.File, want fileIdentity) error {
	fd, err := unix.Openat(int(root.Fd()), "thread-self/ns/pid", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), "launch-active-thread-pid-namespace")
	id, idErr := pidNamespace(f)
	closeErr := f.Close()
	if idErr != nil || closeErr != nil || id != want {
		return errors.Join(errors.New("active thread is outside original launch namespace"), idErr, closeErr)
	}
	return nil
}

// receiveLaunchFrame allocates fixed bounds before receiving. Unexpected
// rights are our newly received handles and are closed, never leaked or used.
func receiveLaunchFrame(fd int, deadline time.Time) ([]byte, unix.Ucred, error) {
	buf := make([]byte, launchFrameLimit+1)
	control := make([]byte, unix.CmsgSpace(unix.SizeofUcred))
	for {
		if !time.Now().Before(deadline) {
			return nil, unix.Ucred{}, errors.New("launch publication deadline exceeded")
		}
		n, nc, flags, _, err := unix.Recvmsg(fd, buf, control, unix.MSG_DONTWAIT|unix.MSG_CMSG_CLOEXEC)
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EINTR) {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				continue
			}
			poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
			_, pollErr := unix.Poll(poll, int((remaining+time.Millisecond-1)/time.Millisecond))
			if pollErr != nil && !errors.Is(pollErr, unix.EINTR) {
				return nil, unix.Ucred{}, pollErr
			}
			continue
		}
		if err != nil {
			return nil, unix.Ucred{}, err
		}
		cred, credErr := packetCredentials(control[:nc])
		if flags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) != 0 || n == 0 || n > launchFrameLimit {
			return nil, unix.Ucred{}, errors.New("missing/truncated/oversize launch packet")
		}
		if credErr != nil {
			return nil, unix.Ucred{}, credErr
		}
		// Never accept a queued second record. Do not peek ancillary data:
		// SCM_RIGHTS can install handles even on a peek.
		n2, nc2, flags2, _, err2 := unix.Recvmsg(fd, make([]byte, 1), control, unix.MSG_DONTWAIT|unix.MSG_CMSG_CLOEXEC)
		if nc2 > 0 {
			_, _ = packetCredentials(control[:nc2])
		}
		if err2 == nil && (n2 != 0 || nc2 != 0 || flags2&(unix.MSG_TRUNC|unix.MSG_CTRUNC|unix.MSG_EOR) != 0) {
			return nil, unix.Ucred{}, errors.New("multiple launch records")
		}
		if err2 != nil && !errors.Is(err2, unix.EAGAIN) {
			return nil, unix.Ucred{}, err2
		}
		return buf[:n], cred, nil
	}
}

func packetCredentials(control []byte) (unix.Ucred, error) {
	messages, err := unix.ParseSocketControlMessage(control)
	if err != nil {
		return unix.Ucred{}, err
	}
	var cred unix.Ucred
	count, foreign := 0, false
	for _, m := range messages {
		if m.Header.Level == unix.SOL_SOCKET && m.Header.Type == unix.SCM_CREDENTIALS {
			value, err := unix.ParseUnixCredentials(&m)
			if err != nil {
				foreign = true
				continue
			}
			cred = *value
			count++
		} else {
			foreign = true
			if m.Header.Level == unix.SOL_SOCKET && m.Header.Type == unix.SCM_RIGHTS {
				rights, err := unix.ParseUnixRights(&m)
				if err == nil {
					for _, fd := range rights {
						_ = unix.Close(fd)
					}
				}
			}
		}
	}
	if foreign || count != 1 {
		return unix.Ucred{}, errors.New("launch packet lacks one original sender credential")
	}
	return cred, nil
}

func strictLaunchRecord(raw []byte, record *launchRecord) error {
	if len(raw) == 0 || len(raw) > launchFrameLimit {
		return errors.New("invalid launch frame size")
	}
	// encoding/json rejects unknown fields but otherwise accepts duplicates.
	// Reject duplicates recursively before typed decoding, with finite depth.
	dec := json.NewDecoder(bytes.NewReader(raw))
	var value func(int) error
	value = func(depth int) error {
		if depth > 4 {
			return errors.New("launch record nesting exceeds schema")
		}
		token, err := dec.Token()
		if err != nil {
			return err
		}
		if delimiter, ok := token.(json.Delim); ok {
			if delimiter != '{' {
				return errors.New("launch record has non-schema collection")
			}
			seen := map[string]bool{}
			for dec.More() {
				key, err := dec.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok || seen[name] {
					return errors.New("duplicate/invalid launch field")
				}
				seen[name] = true
				if err := value(depth + 1); err != nil {
					return err
				}
			}
			end, err := dec.Token()
			if err != nil || end != json.Delim('}') {
				return errors.New("invalid launch object end")
			}
		}
		return nil
	}
	if err := value(0); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("trailing launch record data")
	}
	dec = json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var decoded launchRecord
	if err := dec.Decode(&decoded); err != nil {
		return err
	}
	nonce, err := hex.DecodeString(decoded.Launch)
	if err != nil || len(nonce) != 16 || hex.EncodeToString(nonce) != decoded.Launch {
		return errors.New("launch nonce is not canonical lowercase hex")
	}
	// This private wire format is emitted only by json.Marshal(launchRecord).
	// Require its exact encoding: Go's otherwise case-insensitive struct
	// matching must not accept Version/PID aliases, omitted fields, nulls or
	// duplicate semantic names with a matching last value. Decode privately
	// and publish no partial result on any refusal.
	canonical, err := json.Marshal(decoded)
	if err != nil {
		return err
	}
	if !bytes.Equal(raw, canonical) {
		return errors.New("launch record is not the exact producer encoding")
	}
	*record = decoded
	return nil
}

func cloneProcess(p *processPin) (out *processPin, err error) {
	out = &processPin{identity: p.identity}
	defer func() {
		if err != nil {
			err = errors.Join(err, out.close())
			out = nil
		}
	}()
	for _, pair := range []struct {
		from *os.File
		to   **os.File
	}{{p.directory, &out.directory}, {p.namespace, &out.namespace}, {p.pidfd, &out.pidfd}} {
		*pair.to, err = duplicate(pair.from)
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

func (a *LaunchAnchor) check() error {
	if a == nil || a.closed {
		return errors.New("original launch anchor closed")
	}
	if a.failed != nil {
		return a.failed
	}
	err := a.checkOriginal()
	if err != nil {
		a.failed = err
	}
	return err
}

func (a *LaunchAnchor) checkOriginal() error {
	if a.root == nil || a.child == nil || a.launcher == nil || a.namespace == nil || a.launch == "" {
		return errors.New("incomplete original launch anchor")
	}
	if os.Getpid() != a.child.identity.PID || os.Getppid() != a.launcher.identity.PID {
		return errors.New("launch anchor used outside original child")
	}
	boot, err := bootIdentity(a.root)
	if err != nil || boot != a.boot {
		return errors.Join(errors.New("original anchor boot changed"), err)
	}
	if err := a.launcher.check(); err != nil {
		return err
	}
	if err := a.child.check(); err != nil {
		return err
	}
	id, err := pidNamespace(a.namespace)
	if err != nil || id != a.launcher.identity.Namespace || id != a.child.identity.Namespace {
		return errors.Join(errors.New("original launch namespace changed"), err)
	}
	return checkActiveNamespace(a.root, id)
}

// Check revalidates original retained handles and the active calling thread's
// namespace. A failed original check is sticky; current matching facts cannot
// revive a lost launcher or child.
func (a *LaunchAnchor) Check() error {
	if a == nil {
		return errors.New("missing original launch anchor")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.check()
}

func (a *LaunchAnchor) Close() error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil
	}
	a.closed = true
	return errors.Join(closeFiles(a.root, a.namespace), a.launcher.close(), a.child.close())
}

// Duplicate copies original retained handles, never reopens matching numeric
// identities as substitute custody. It remains bound to this controller.
func (a *LaunchAnchor) Duplicate() (out *LaunchAnchor, err error) {
	if a == nil {
		return nil, errors.New("missing original launch anchor")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	a.mu.Lock()
	defer a.mu.Unlock()
	if err = a.check(); err != nil {
		return nil, err
	}
	out = &LaunchAnchor{boot: a.boot, launch: a.launch}
	defer func() {
		if err != nil {
			err = errors.Join(err, out.Close())
			out = nil
		}
	}()
	out.root, err = duplicate(a.root)
	if err != nil {
		return out, err
	}
	out.namespace, err = duplicate(a.namespace)
	if err != nil {
		return out, err
	}
	out.launcher, err = cloneProcess(a.launcher)
	if err != nil {
		return out, err
	}
	out.child, err = cloneProcess(a.child)
	if err != nil {
		return out, err
	}
	if err = a.check(); err != nil {
		return out, err
	}
	err = out.check()
	return out, err
}

// DuplicatePIDNamespace returns one owned CLOEXEC typed NSFS duplicate only
// after full original-anchor checks before and after. It is an observation
// handle, not independent admission or a receipt/authority token.
func (a *LaunchAnchor) DuplicatePIDNamespace() (out *os.File, err error) {
	if a == nil {
		return nil, errors.New("missing original launch anchor")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	a.mu.Lock()
	defer a.mu.Unlock()
	if err = a.check(); err != nil {
		return nil, err
	}
	out, err = duplicate(a.namespace)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, out.Close())
			out = nil
		}
	}()
	id, err := pidNamespace(out)
	if err != nil || id != a.child.identity.Namespace {
		return out, errors.Join(errors.New("namespace duplicate changed identity"), err)
	}
	err = a.check()
	return out, err
}
