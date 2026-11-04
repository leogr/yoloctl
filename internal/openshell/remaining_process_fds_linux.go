package openshell

import (
	"context"
	"errors"
	"io"
	"os"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"sync"

	"golang.org/x/sys/unix"
)

const nativeFDReferenceLimit = 65536
const nativeFDLinkLimit = 4096
const nativeFDReadBytesLimit = 16 << 20

// PRIMARY OBSERVATION ONLY. Matching device/inode/mount/link text does NOT
// establish an OFD, files_struct or original-acquisition custody. No records
// here are exemptions, and no cleanup/admission caller is installed.
type nativeProcessFDReference struct {
	pid, tid, fd          int
	start                 string
	task                  pinFileIdentity
	linkDevice, linkInode uint64
	device, inode, mount  uint64
	kind                  uint32
	target                string // private raw kernel link text; never diagnostic rendering
}

func (nativeProcessFDReference) String() string     { return "task FD reference [observation only]" }
func (r nativeProcessFDReference) GoString() string { return r.String() }

type nativeProcessFDTable struct {
	thread    nativeProcessThread // exact borrowed original task directory
	directory *os.File            // own distinct original enumeration description
	identity  pinFileIdentity
}

type nativeProcessFDObservation struct {
	mu                sync.Mutex
	universe          *nativeProcessUniverse // borrowed, never closed by this object
	tables            []nativeProcessFDTable
	references        []nativeProcessFDReference
	complete, closed  bool
	failure, closeErr error
}

func (*nativeProcessFDObservation) String() string {
	return "task FD observations [not consumer proof]"
}
func (p *nativeProcessFDObservation) GoString() string { return p.String() }

// The caller MUST hold its outer LockOSThread through universe capture,
// this observation, later matching/rechecks and Close. These inner locks do not
// extend that caller lifetime. Independently admitted VM PID-anchor comparison
// must precede interpreting this proc view; this function supplies no such grant.
// All own fd-directory handles are acquired BEFORE either reference pass, so
// the current process sees the same collector-owned descriptor set in both.
func readNativeProcessFDObservation(ctx context.Context, u *nativeProcessUniverse) (_ *nativeProcessFDObservation, err error) {
	if u == nil {
		return nil, errors.New("missing original process universe")
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	ctx, cancel := context.WithTimeout(ctx, nativeProcessReadBudget)
	defer cancel()
	if u.closed || !u.complete || u.failure != nil {
		return nil, errors.Join(errors.New("process universe unavailable"), u.failure)
	}
	if err := u.check(ctx); err != nil {
		u.failure = err
		return nil, err
	}
	p := &nativeProcessFDObservation{universe: u}
	defer func() {
		if err != nil {
			err = errors.Join(err, p.Close())
		}
	}()
	if len(u.threads) == 0 || len(u.threads) > nativeProcessThreadLimit {
		return nil, errors.New("missing or unbounded original tasks")
	}
	for _, thread := range u.threads {
		directory, openErr := u.openDirectory(ctx, thread.directory, "fd")
		if openErr != nil {
			return nil, openErr
		} // missing is NOT an empty table
		p.tables = append(p.tables, nativeProcessFDTable{thread: thread, directory: directory})
		table := &p.tables[len(p.tables)-1]
		table.identity, err = u.sameProcObject(directory, true)
		if err != nil {
			return nil, err
		}
	}
	first, err := p.readPass(ctx)
	if err != nil {
		return nil, err
	}
	if err := u.check(ctx); err != nil {
		u.failure = err
		return nil, err
	}
	second, err := p.readPass(ctx)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(first, second) {
		return nil, errors.New("task FD references changed across observation")
	}
	if err := u.check(ctx); err != nil {
		u.failure = err
		return nil, err
	}
	p.references, p.complete = second, true
	return p, nil
}

// Caller holds p.mu then u.mu. Do not call exported u.Check while holding u.mu.
func (p *nativeProcessFDObservation) Check(ctx context.Context) error {
	if p == nil {
		return errors.New("nil task FD observation")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || !p.complete || p.failure != nil || p.universe == nil {
		return errors.Join(errors.New("task FD observation closed/incomplete/failed"), p.failure)
	}
	u := p.universe
	u.mu.Lock()
	defer u.mu.Unlock()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	ctx, cancel := context.WithTimeout(ctx, nativeProcessReadBudget)
	defer cancel()
	p.failure = func() error {
		if u.closed || !u.complete || u.failure != nil {
			return errors.Join(errors.New("original process universe unavailable"), u.failure)
		}
		if err := u.check(ctx); err != nil {
			u.failure = err
			return err
		}
		for pass := 0; pass < 2; pass++ {
			references, err := p.readPass(ctx)
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(p.references, references) {
				return errors.New("original task FD reference changed")
			}
			if err := u.check(ctx); err != nil {
				u.failure = err
				return err
			}
		}
		return nil
	}()
	return p.failure
}

func (p *nativeProcessFDObservation) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return p.closeErr
	}
	p.closed, p.complete = true, false
	for i := range p.tables {
		if p.tables[i].directory != nil {
			p.closeErr = errors.Join(p.closeErr, p.tables[i].directory.Close())
			p.tables[i].directory = nil
		}
	}
	p.references = nil
	p.universe = nil
	return p.closeErr
}

func (p *nativeProcessFDObservation) readPass(ctx context.Context) ([]nativeProcessFDReference, error) {
	u := p.universe
	var result []nativeProcessFDReference
	remaining := nativeFDReadBytesLimit
	for _, table := range p.tables {
		if err := u.currentDirectory(ctx, table.thread.directory, "fd", table.directory, table.identity); err != nil {
			return nil, err
		}
		numbers, err := nativeFDNumbers(ctx, table.directory, nativeFDReferenceLimit-len(result))
		if err != nil {
			return nil, err
		}
		for _, fd := range numbers {
			if len(result) >= nativeFDReferenceLimit {
				return nil, errors.New("task FD reference count exceeded")
			}
			reference, err := nativeReadFDTarget(ctx, table, fd)
			if err != nil {
				return nil, err
			}
			remaining -= len(reference.target) + 96
			if remaining < 0 {
				return nil, errors.New("task FD observation byte budget exceeded")
			}
			result = append(result, reference)
		}
		if err := u.currentDirectory(ctx, table.thread.directory, "fd", table.directory, table.identity); err != nil {
			return nil, err
		}
		after, err := nativeFDNumbers(ctx, table.directory, nativeFDReferenceLimit)
		if err != nil || !slices.Equal(numbers, after) {
			return nil, errors.Join(errors.New("task FD roster changed"), err)
		}
	}
	return result, ctx.Err()
}

func nativeFDNumber(name string) (int, error) {
	n, err := strconv.ParseUint(name, 10, 31)
	if err != nil || strconv.FormatUint(n, 10) != name {
		return 0, errors.New("noncanonical task FD slot")
	}
	return int(n), nil // zero is a genuine descriptor; nativeProcessNumber rejects it
}

// Seek is on this collector-owned independent open description only. Go's
// File.Seek clears Readdirnames buffering before resetting the kernel offset;
// never use dup/F_DUPFD or a borrowed enumeration offset for these passes.
func nativeFDNumbers(ctx context.Context, directory *os.File, limit int) ([]int, error) {
	if directory == nil || limit < 0 {
		return nil, errors.New("task FD directory/budget unavailable")
	}
	if _, err := directory.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	numbers := []int{}
	seen := map[int]bool{}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		names, readErr := directory.Readdirnames(128)
		for _, name := range names {
			n, err := nativeFDNumber(name)
			if err != nil || seen[n] {
				return nil, errors.Join(errors.New("ambiguous task FD roster"), err)
			}
			if len(numbers) >= limit {
				return nil, errors.New("task FD reference count exceeded")
			}
			seen[n] = true
			numbers = append(numbers, n)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
	}
	slices.Sort(numbers)
	return numbers, nil // genuine successfully enumerated empty table is valid
}

func nativeReadFDTarget(ctx context.Context, table nativeProcessFDTable, fd int) (nativeProcessFDReference, error) {
	fail := func(err error) (nativeProcessFDReference, error) {
		return nativeProcessFDReference{}, errors.Join(errors.New("task FD link/target changed or unavailable"), err)
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if table.directory == nil || fd < 0 {
		return fail(nil)
	}
	name := strconv.Itoa(fd)
	directory := int(table.directory.Fd())
	var linkBefore, linkAfter unix.Stat_t
	if err := unix.Fstatat(directory, name, &linkBefore, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return fail(err)
	}
	if linkBefore.Mode&unix.S_IFMT != unix.S_IFLNK {
		return fail(nil)
	}
	readLink := func() (string, error) {
		raw := make([]byte, nativeFDLinkLimit+1)
		n, err := unix.Readlinkat(directory, name, raw)
		if err != nil || n <= 0 || n > nativeFDLinkLimit {
			return "", errors.Join(errors.New("missing/unbounded task FD link"), err)
		}
		return string(raw[:n]), nil
	}
	target, err := readLink()
	if err != nil {
		return fail(err)
	}
	var before, after unix.Statx_t
	mask := uint32(unix.STATX_TYPE | unix.STATX_INO | unix.STATX_MNT_ID)
	// Following this fixed numeric proc magic link performs metadata lookup,
	// never target open/read/dup (so FIFO/device targets cannot start I/O).
	if err := unix.Statx(directory, name, 0, int(mask), &before); err != nil {
		return fail(err)
	}
	repeated, err := readLink()
	if err != nil || repeated != target {
		return fail(err)
	}
	if err := unix.Statx(directory, name, 0, int(mask), &after); err != nil {
		return fail(err)
	}
	if err := unix.Fstatat(directory, name, &linkAfter, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return fail(err)
	}
	if linkBefore.Dev != linkAfter.Dev || linkBefore.Ino != linkAfter.Ino || linkBefore.Mode != linkAfter.Mode {
		return fail(nil)
	}
	if before.Mask&mask != mask || after.Mask&mask != mask || before.Ino == 0 || before.Mnt_id == 0 || before.Ino != after.Ino || before.Mnt_id != after.Mnt_id || before.Dev_major != after.Dev_major || before.Dev_minor != after.Dev_minor || before.Mode&unix.S_IFMT != after.Mode&unix.S_IFMT {
		return fail(nil)
	}
	kind := uint32(before.Mode) & unix.S_IFMT
	if kind == 0 {
		return fail(nil)
	}
	return nativeProcessFDReference{pid: table.thread.pid, tid: table.thread.tid, fd: fd, start: table.thread.start, task: table.thread.identity, linkDevice: uint64(linkBefore.Dev), linkInode: linkBefore.Ino, device: unix.Mkdev(before.Dev_major, before.Dev_minor), inode: before.Ino, mount: before.Mnt_id, kind: kind, target: target}, ctx.Err()
}
