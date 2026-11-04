package openshell

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const (
	nativeProcessGroupLimit  = 4096
	nativeProcessThreadLimit = 32768
	nativeProcessEntryLimit  = 65536
	nativeProcessReadBudget  = 10 * time.Second
)

type nativeProcessThread struct {
	pid, tid             int
	start                string
	directory            *os.File // Exact original task directory, never an alias replacement.
	identity             pinFileIdentity
	zombie, kernelThread bool
	nspids               []int
}

type nativeProcessGroup struct {
	pid                     int
	start                   string
	zombie                  bool
	directory, tasks, pidfd *os.File
	identity, tasksID       pinFileIdentity
	tids                    []int
}

// These facts describe only the observed proc view. Even an internally
// consistent nested view is NOT VM admission. The consumer must compare these
// retained observations against its independently admitted launch/VM anchor.
type nativeProcessView struct {
	proc                   nativeProcessProcMount
	boot                   string
	readerPID, readerTID   int
	readerStart, initStart string
	readerID, initID       pinFileIdentity
	readerPIDNamespace     namespaceIdentity
	initPIDNamespace       namespaceIdentity
}

type nativeProcessUniverse struct {
	mu                 sync.Mutex
	proc, reader, init *os.File
	readerNS, initNS   *os.File
	view               nativeProcessView
	processes          []nativeProcessGroup
	threads            []nativeProcessThread
	ops                nativeProcessUniverseOps
	complete, closed   bool
	failure, closeErr  error
}

// Private fixtures replace only kernel authentication/pidfd/reader facts. The
// public entry below always installs the real operations; fixtures confer no
// positive procfs, VM admission or native-completeness evidence.
type nativeProcessUniverseOps struct {
	identity    func(*os.File, bool) (pinFileIdentity, error)
	namespace   func(*os.File) (*os.File, namespaceIdentity, error)
	namespaceID func(*os.File) (namespaceIdentity, error)
	pidfd       func(int) (*os.File, error)
	checkPIDFD  func(context.Context, *os.File, *os.File, int, bool) error
	readerIDs   func() (int, int)
	boot        func(*os.File) (string, error)
}

func nativeProcessKernelOps() nativeProcessUniverseOps {
	return nativeProcessUniverseOps{
		identity: nativeProcessProcIdentity, namespace: openNativeProcessPIDNamespace,
		namespaceID: nativeProcessPIDNamespaceIdentity,
		pidfd: func(pid int) (*os.File, error) {
			fd, err := unix.PidfdOpen(pid, 0) // TGID only; every TID is pinned separately below.
			if err != nil {
				return nil, err
			}
			return os.NewFile(uintptr(fd), "remaining-original-pidfd"), nil
		},
		checkPIDFD: checkNativeProcessPIDFD,
		readerIDs:  func() (int, int) { return os.Getpid(), unix.Gettid() },
		boot:       func(proc *os.File) (string, error) { return readKernelBootID(int(proc.Fd())) },
	}
}

// readNativeProcessUniverse borrows proc and owns every new handle. Its result
// is a bracketed observation, not an atomic snapshot, reference-absence ACK or
// future-acquisition fence. No UID/namespace/command/box filter is applied.
// The caller must keep its OS thread locked through capture, matching and
// Check; Check refuses another reader TID. Close is synchronized and idempotent.
// Counts, bytes and retained FDs are bounded. The time budget is cooperative
// between synchronous kernel reads; it is not a hard bound on kernel latency.
func readNativeProcessUniverse(ctx context.Context, proc *os.File) (*nativeProcessUniverse, error) {
	return readNativeProcessUniverseWithOps(ctx, proc, nativeProcessKernelOps())
}

func readNativeProcessUniverseWithOps(ctx context.Context, proc *os.File, ops nativeProcessUniverseOps) (_ *nativeProcessUniverse, err error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	ctx, cancel := context.WithTimeout(ctx, nativeProcessReadBudget)
	defer cancel()
	u := &nativeProcessUniverse{ops: ops}
	defer func() {
		if err != nil {
			err = errors.Join(err, u.Close())
		}
	}()
	if proc == nil || ops.identity == nil || ops.namespace == nil || ops.namespaceID == nil || ops.pidfd == nil || ops.checkPIDFD == nil || ops.readerIDs == nil || ops.boot == nil {
		return nil, errors.New("incomplete process-universe reader")
	}
	borrowed, err := ops.identity(proc, true)
	if err != nil {
		return nil, err
	}
	// openat(".") makes a separate open description. dup would share getdents
	// position with the borrowed root and could turn a repeated scan into EOF.
	u.proc, err = nativeProcessOpenDirectory(proc, ".")
	if err != nil {
		return nil, err
	}
	owned, err := ops.identity(u.proc, true)
	if err != nil || owned != borrowed {
		return nil, errors.New("borrowed proc mapping changed while opening owned view")
	}
	u.view.proc.identity = owned
	u.view.readerPID, u.view.readerTID = ops.readerIDs()
	u.init, err = u.openDirectory(ctx, u.proc, "1")
	if err != nil {
		return nil, err
	}
	// Intentional fixed kernel magic link, only after genuine proc authentication.
	fd, err := unix.Openat(int(u.proc.Fd()), "thread-self", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	u.reader = os.NewFile(uintptr(fd), "remaining-original-reader")
	if _, err := u.sameProcObject(u.reader, true); err != nil {
		return nil, err
	}
	u.readerNS, u.view.readerPIDNamespace, err = ops.namespace(u.reader)
	if err != nil {
		return nil, err
	}
	u.initNS, u.view.initPIDNamespace, err = ops.namespace(u.init)
	if err != nil {
		return nil, err
	}
	if u.view.readerPIDNamespace != u.view.initPIDNamespace {
		return nil, errors.New("reader PID namespace differs from observed proc PID1")
	}
	u.view.readerID, err = u.sameProcObject(u.reader, true)
	if err != nil {
		return nil, err
	}
	u.view.initID, err = u.sameProcObject(u.init, true)
	if err != nil {
		return nil, err
	}
	reader, _, err := u.taskMetadata(ctx, u.reader, u.view.readerPID, u.view.readerTID)
	if err != nil || reader.zombie {
		return nil, errors.Join(errors.New("reader process mapping is unavailable"), err)
	}
	init, _, err := u.taskMetadata(ctx, u.init, 1, 1)
	if err != nil || init.zombie {
		return nil, errors.Join(errors.New("observed PID1 mapping is unavailable"), err)
	}
	u.view.readerStart, u.view.initStart = reader.start, init.start
	u.view.boot, err = ops.boot(u.proc)
	if err != nil {
		return nil, err
	}
	raw, err := u.readMetadata(ctx, u.reader, "mountinfo", 4<<20)
	if err != nil {
		return nil, err
	}
	u.view.proc, err = decodeNativeProcessProcMount(raw, owned)
	if err != nil {
		return nil, err
	}
	pids, err := u.members(ctx, u.proc, true, nativeProcessGroupLimit)
	if err != nil || !slices.Contains(pids, 1) || !slices.Contains(pids, u.view.readerPID) {
		return nil, errors.Join(errors.New("proc roster lacks reader or PID1"), err)
	}
	for _, pid := range pids {
		if err := u.captureGroup(ctx, pid); err != nil {
			return nil, fmt.Errorf("capture TGID %d: %w", pid, err)
		}
	}
	if err := u.check(ctx); err != nil {
		return nil, err
	}
	u.complete = true
	return u, nil
}

func (u *nativeProcessUniverse) captureGroup(ctx context.Context, pid int) error {
	// Append ownership before subsequent fallible operations, for partial Close.
	u.processes = append(u.processes, nativeProcessGroup{pid: pid})
	p := &u.processes[len(u.processes)-1]
	var err error
	p.directory, err = u.openDirectory(ctx, u.proc, strconv.Itoa(pid))
	if err != nil {
		return err
	}
	p.identity, err = u.sameProcObject(p.directory, true)
	if err != nil {
		return err
	}
	stat, _, err := u.taskMetadata(ctx, p.directory, pid, pid)
	if err != nil {
		return err
	}
	p.start, p.zombie = stat.start, stat.zombie
	p.pidfd, err = u.ops.pidfd(pid)
	if err != nil {
		return err
	}
	p.tasks, err = u.openDirectory(ctx, p.directory, "task")
	if err != nil {
		// A terminated main thread can make task/ unavailable. Refuse the
		// capture; never interpret it as a process with no remaining threads.
		return err
	}
	p.tasksID, err = u.sameProcObject(p.tasks, true)
	if err != nil {
		return err
	}
	p.tids, err = u.members(ctx, p.tasks, false, nativeProcessThreadLimit-len(u.threads))
	if err != nil || !slices.Contains(p.tids, pid) {
		return errors.Join(errors.New("task roster lacks its original leader"), err)
	}
	for _, tid := range p.tids {
		u.threads = append(u.threads, nativeProcessThread{pid: pid, tid: tid})
		t := &u.threads[len(u.threads)-1]
		t.directory, err = u.openDirectory(ctx, p.tasks, strconv.Itoa(tid))
		if err != nil {
			return err
		}
		t.identity, err = u.sameProcObject(t.directory, true)
		if err != nil {
			return err
		}
		stat, status, err := u.taskMetadata(ctx, t.directory, pid, tid)
		if err != nil {
			return err
		}
		t.start, t.zombie, t.kernelThread = stat.start, stat.zombie, status.kernelThread
		t.nspids = slices.Clone(status.nspids)
	}
	return nil
}

// Check is sticky on failure: an object that observed uncertainty cannot later
// adopt a replacement or regain completeness. Close must still be called.
func (u *nativeProcessUniverse) Check(ctx context.Context) error {
	if u == nil {
		return errors.New("nil process universe")
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed || !u.complete || u.failure != nil {
		return errors.Join(errors.New("process universe is closed, incomplete or failed"), u.failure)
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	ctx, cancel := context.WithTimeout(ctx, nativeProcessReadBudget)
	defer cancel()
	u.failure = u.check(ctx)
	return u.failure
}

func (u *nativeProcessUniverse) check(ctx context.Context) error {
	if err := u.checkView(ctx); err != nil {
		return err
	}
	wantPIDs := make([]int, len(u.processes))
	for i, p := range u.processes {
		wantPIDs[i] = p.pid
	}
	for pass := 0; pass < 2; pass++ {
		pids, err := u.members(ctx, u.proc, true, nativeProcessGroupLimit)
		if err != nil || !slices.Equal(pids, wantPIDs) {
			return errors.Join(errors.New("process membership changed or is unavailable"), err)
		}
		for _, p := range u.processes {
			if err := u.currentDirectory(ctx, u.proc, strconv.Itoa(p.pid), p.directory, p.identity); err != nil {
				return err
			}
			stat, _, err := u.taskMetadata(ctx, p.directory, p.pid, p.pid)
			if err != nil || stat.start != p.start || stat.zombie != p.zombie {
				return errors.Join(errors.New("original TGID identity/state changed"), err)
			}
			if err := u.ops.checkPIDFD(ctx, u.proc, p.pidfd, p.pid, p.zombie); err != nil {
				return err
			}
			if err := u.currentDirectory(ctx, p.directory, "task", p.tasks, p.tasksID); err != nil {
				return err
			}
			tids, err := u.members(ctx, p.tasks, false, nativeProcessThreadLimit)
			if err != nil || !slices.Equal(tids, p.tids) {
				return errors.Join(errors.New("task membership changed or is unavailable"), err)
			}
		}
		for _, t := range u.threads {
			index, found := slices.BinarySearch(wantPIDs, t.pid)
			if !found {
				return errors.New("task has no original TGID")
			}
			if err := u.currentDirectory(ctx, u.processes[index].tasks, strconv.Itoa(t.tid), t.directory, t.identity); err != nil {
				return err
			}
			stat, status, err := u.taskMetadata(ctx, t.directory, t.pid, t.tid)
			if err != nil || stat.start != t.start || stat.zombie != t.zombie || status.kernelThread != t.kernelThread || !slices.Equal(status.nspids, t.nspids) {
				return errors.Join(errors.New("original TID identity/state/mapping changed"), err)
			}
		}
		// Bracket the per-object reads with complete memberships, including
		// the final pass. These are consistency checks, not atomicity.
		pids, err = u.members(ctx, u.proc, true, nativeProcessGroupLimit)
		if err != nil || !slices.Equal(pids, wantPIDs) {
			return errors.Join(errors.New("process membership changed across identity reads"), err)
		}
		for _, p := range u.processes {
			tids, err := u.members(ctx, p.tasks, false, nativeProcessThreadLimit)
			if err != nil || !slices.Equal(tids, p.tids) {
				return errors.Join(errors.New("task membership changed across identity reads"), err)
			}
		}
	}
	return u.checkView(ctx)
}

func (u *nativeProcessUniverse) checkView(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	pid, tid := u.ops.readerIDs()
	if pid != u.view.readerPID || tid != u.view.readerTID {
		return errors.New("process universe used by another reader PID/TID")
	}
	id, err := u.sameProcObject(u.proc, true)
	if err != nil || id != u.view.proc.identity {
		return errors.Join(errors.New("original proc view mapping changed"), err)
	}
	if err := u.currentDirectory(ctx, u.proc, "1", u.init, u.view.initID); err != nil {
		return err
	}
	reader, err := nativeProcessOpenMagicReader(u.proc)
	if err != nil {
		return err
	}
	readerID, identityErr := u.sameProcObject(reader, true)
	closeErr := reader.Close()
	if identityErr != nil || closeErr != nil || readerID != u.view.readerID {
		return errors.Join(errors.New("current reader differs from original proc task"), identityErr, closeErr)
	}
	for _, item := range []struct {
		dir, ns  *os.File
		want     namespaceIdentity
		pid, tid int
		start    string
	}{{u.reader, u.readerNS, u.view.readerPIDNamespace, pid, tid, u.view.readerStart}, {u.init, u.initNS, u.view.initPIDNamespace, 1, 1, u.view.initStart}} {
		original, err := u.ops.namespaceID(item.ns)
		if err != nil || original != item.want {
			return errors.Join(errors.New("original PID namespace descriptor changed"), err)
		}
		ns, actual, err := u.ops.namespace(item.dir)
		if err != nil {
			if ns != nil {
				err = errors.Join(err, ns.Close())
			}
			return err
		}
		closeErr := ns.Close()
		if closeErr != nil || actual != item.want {
			return errors.Join(errors.New("observed PID namespace changed"), closeErr)
		}
		stat, _, err := u.taskMetadata(ctx, item.dir, item.pid, item.tid)
		if err != nil || stat.start != item.start || stat.zombie {
			return errors.Join(errors.New("reader/PID1 start identity changed"), err)
		}
	}
	boot, err := u.ops.boot(u.proc)
	if err != nil || boot != u.view.boot {
		return errors.Join(errors.New("kernel boot observation changed"), err)
	}
	raw, err := u.readMetadata(ctx, u.reader, "mountinfo", 4<<20)
	if err != nil {
		return err
	}
	mount, err := decodeNativeProcessProcMount(raw, id)
	if err != nil || mount != u.view.proc {
		return errors.Join(errors.New("observed proc mount changed or is filtered"), err)
	}
	return ctx.Err()
}

func (u *nativeProcessUniverse) Close() error {
	if u == nil {
		return nil
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed {
		return u.closeErr
	}
	u.closed, u.complete = true, false
	closeFile := func(file **os.File) {
		if *file != nil {
			u.closeErr = errors.Join(u.closeErr, (*file).Close())
			*file = nil
		}
	}
	for i := range u.threads {
		closeFile(&u.threads[i].directory)
	}
	for i := range u.processes {
		closeFile(&u.processes[i].pidfd)
		closeFile(&u.processes[i].tasks)
		closeFile(&u.processes[i].directory)
	}
	for _, file := range []**os.File{&u.initNS, &u.readerNS, &u.init, &u.reader, &u.proc} {
		closeFile(file)
	}
	return u.closeErr
}

func nativeProcessOpenDirectory(parent *os.File, name string) (*os.File, error) {
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), "remaining-proc-directory"), nil
}

func nativeProcessOpenMagicReader(proc *os.File) (*os.File, error) {
	fd, err := unix.Openat(int(proc.Fd()), "thread-self", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), "remaining-current-reader"), nil
}

func (u *nativeProcessUniverse) sameProcObject(file *os.File, directory bool) (pinFileIdentity, error) {
	id, err := u.ops.identity(file, directory)
	if err != nil || id.device != u.view.proc.identity.device || id.mount != u.view.proc.identity.mount {
		return pinFileIdentity{}, errors.Join(errors.New("process object leaves retained proc mount"), err)
	}
	return id, nil
}

func (u *nativeProcessUniverse) openDirectory(ctx context.Context, parent *os.File, name string) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := nativeProcessOpenDirectory(parent, name)
	if err != nil {
		return nil, err
	}
	if _, err := u.sameProcObject(file, true); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return file, nil
}

func (u *nativeProcessUniverse) currentDirectory(ctx context.Context, parent *os.File, name string, original *os.File, want pinFileIdentity) error {
	id, err := u.sameProcObject(original, true)
	if err != nil || id != want {
		return errors.Join(errors.New("original process/task directory changed"), err)
	}
	current, err := u.openDirectory(ctx, parent, name)
	if err != nil {
		return err
	}
	id, err = u.sameProcObject(current, true)
	closeErr := current.Close()
	if err != nil || closeErr != nil || id != want {
		return errors.Join(errors.New("process/task alias no longer names original directory"), err, closeErr)
	}
	return nil
}

func (u *nativeProcessUniverse) members(ctx context.Context, original *os.File, root bool, limit int) (_ []int, err error) {
	if limit <= 0 {
		return nil, errors.New("process/thread resource limit exceeded")
	}
	file, err := u.openDirectory(ctx, original, ".")
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	want, err := u.sameProcObject(original, true)
	if err != nil {
		return nil, err
	}
	actual, err := u.sameProcObject(file, true)
	if err != nil || actual != want {
		return nil, errors.Join(errors.New("enumeration directory changed"), err)
	}
	var result []int
	entries := 0
	seen := make(map[int]bool)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		names, readErr := file.Readdirnames(128)
		entries += len(names)
		if entries > nativeProcessEntryLimit {
			return nil, errors.New("process directory entry limit exceeded")
		}
		for _, name := range names {
			n, numeric, err := nativeProcessNumber(name)
			if err != nil || !root && !numeric || seen[n] && numeric {
				return nil, errors.Join(errors.New("ambiguous process/task roster entry"), err)
			}
			if numeric {
				seen[n] = true
				result = append(result, n)
				if len(result) > limit {
					return nil, errors.New("process/thread resource limit exceeded")
				}
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
	}
	if len(result) == 0 {
		return nil, errors.New("empty process/task roster is incomplete")
	}
	slices.Sort(result)
	return result, ctx.Err()
}

func (u *nativeProcessUniverse) readMetadata(ctx context.Context, directory *os.File, name string, limit int64) (_ []byte, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fd, err := unix.Openat(int(directory.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "remaining-proc-metadata")
	defer func() { err = errors.Join(err, file.Close()) }()
	if _, err := u.sameProcObject(file, false); err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || len(raw) == 0 || int64(len(raw)) > limit {
		return nil, errors.Join(errors.New("missing or unbounded proc metadata"), err)
	}
	return raw, ctx.Err()
}

func (u *nativeProcessUniverse) taskMetadata(ctx context.Context, directory *os.File, pid, tid int) (nativeProcessStat, nativeProcessStatus, error) {
	readStat := func() (nativeProcessStat, error) {
		raw, err := u.readMetadata(ctx, directory, "stat", nativeProcessMetadataLimit)
		if err != nil {
			return nativeProcessStat{}, err
		}
		return decodeNativeProcessStat(raw, tid)
	}
	before, err := readStat()
	if err != nil {
		return nativeProcessStat{}, nativeProcessStatus{}, err
	}
	raw, err := u.readMetadata(ctx, directory, "status", nativeProcessMetadataLimit)
	if err != nil {
		return nativeProcessStat{}, nativeProcessStatus{}, err
	}
	status, err := decodeNativeProcessStatus(raw, pid, tid)
	if err != nil {
		return nativeProcessStat{}, nativeProcessStatus{}, err
	}
	after, err := readStat()
	if err != nil || before != after {
		return nativeProcessStat{}, nativeProcessStatus{}, errors.Join(errors.New("task changed while reading metadata"), err)
	}
	return before, status, nil
}

func nativeProcessProcIdentity(file *os.File, directory bool) (pinFileIdentity, error) {
	if file == nil {
		return pinFileIdentity{}, errors.New("closed process descriptor")
	}
	var fs unix.Statfs_t
	var st unix.Stat_t
	var sx unix.Statx_t
	kind := uint32(unix.S_IFREG)
	if directory {
		kind = unix.S_IFDIR
	}
	fd := int(file.Fd())
	if unix.Fstatfs(fd, &fs) != nil || fs.Type != unix.PROC_SUPER_MAGIC || unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != kind || unix.Statx(fd, "", unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW, unix.STATX_MNT_ID, &sx) != nil || sx.Mask&unix.STATX_MNT_ID == 0 || sx.Mnt_id == 0 || st.Dev == 0 || st.Ino == 0 {
		return pinFileIdentity{}, errors.New("object is not a mapped genuine kernel proc object")
	}
	// Target processes may have any UID/GID. Ownership is not visibility proof.
	return pinFileIdentity{device: uint64(st.Dev), inode: st.Ino, mount: sx.Mnt_id}, nil
}

func openNativeProcessPIDNamespace(process *os.File) (*os.File, namespaceIdentity, error) {
	parentID, err := nativeProcessProcIdentity(process, true)
	if err != nil {
		return nil, namespaceIdentity{}, err
	}
	ns, err := nativeProcessOpenDirectory(process, "ns")
	if err != nil {
		return nil, namespaceIdentity{}, err
	}
	nsID, err := nativeProcessProcIdentity(ns, true)
	if err != nil || nsID.device != parentID.device || nsID.mount != parentID.mount {
		return nil, namespaceIdentity{}, errors.Join(errors.New("PID namespace path leaves proc mount"), err, ns.Close())
	}
	fd, err := unix.Openat(int(ns.Fd()), "pid", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	closeErr := ns.Close()
	if err != nil {
		return nil, namespaceIdentity{}, errors.Join(err, closeErr)
	}
	file := os.NewFile(uintptr(fd), "remaining-original-pid-namespace")
	if closeErr != nil {
		return nil, namespaceIdentity{}, errors.Join(closeErr, file.Close())
	}
	id, err := nativeProcessPIDNamespaceIdentity(file)
	if err != nil {
		return nil, namespaceIdentity{}, errors.Join(err, file.Close())
	}
	return file, id, nil
}

func nativeProcessPIDNamespaceIdentity(file *os.File) (namespaceIdentity, error) {
	if file == nil {
		return namespaceIdentity{}, errors.New("closed PID namespace descriptor")
	}
	var fs unix.Statfs_t
	var st unix.Stat_t
	fd := int(file.Fd())
	kind, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), 0xb703, 0) // NS_GET_NSTYPE
	if errno != 0 || kind != unix.CLONE_NEWPID || unix.Fstatfs(fd, &fs) != nil || fs.Type != unix.NSFS_MAGIC || unix.Fstat(fd, &st) != nil || st.Dev == 0 || st.Ino == 0 {
		return namespaceIdentity{}, errors.New("descriptor is not a typed kernel PID namespace")
	}
	return namespaceIdentity{Device: uint64(st.Dev), Inode: st.Ino}, nil
}

func checkNativeProcessPIDFD(ctx context.Context, proc, pidfd *os.File, pid int, zombie bool) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if pidfd == nil {
		return errors.New("missing original TGID pidfd")
	}
	poll := []unix.PollFd{{Fd: int32(pidfd.Fd()), Events: unix.POLLIN}}
	if _, err := unix.Poll(poll, 0); err != nil {
		return err
	}
	if poll[0].Revents&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 || !zombie && poll[0].Revents&unix.POLLIN != 0 {
		return errors.New("original TGID exited or its pidfd is unavailable")
	}
	// Authenticate the actual pidfd's numeric target in this proc view. A
	// readable zombie is retained explicitly, never classified as harmless.
	reader, err := nativeProcessOpenMagicReader(proc)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, reader.Close()) }()
	u := &nativeProcessUniverse{proc: proc, ops: nativeProcessKernelOps()}
	u.view.proc.identity, err = nativeProcessProcIdentity(proc, true)
	if err != nil {
		return err
	}
	if _, err := u.sameProcObject(reader, true); err != nil {
		return err
	}
	fdinfo, err := u.openDirectory(ctx, reader, "fdinfo")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, fdinfo.Close()) }()
	raw, err := u.readMetadata(ctx, fdinfo, strconv.Itoa(int(pidfd.Fd())), nativeProcessMetadataLimit)
	if err != nil {
		return err
	}
	var values []string
	for _, line := range strings.Split(string(raw), "\n") {
		if value, ok := strings.CutPrefix(line, "Pid:"); ok {
			values = append(values, strings.TrimSpace(value))
		}
	}
	if !reflect.DeepEqual(values, []string{strconv.Itoa(pid)}) {
		return errors.New("original pidfd target differs from retained TGID/proc view")
	}
	return ctx.Err()
}
