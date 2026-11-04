package openshell

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/sys/unix"
)

type nativeOriginAncestryState uint8

const (
	nativeOriginAncestryUnknown nativeOriginAncestryState = iota
	nativeOriginAncestryMissingRuntime
	nativeOriginAncestryMissingControl
	nativeOriginAncestryMissingContainers
	nativeOriginAncestryPresent
)

type nativeOriginAncestryID struct {
	file pinFileIdentity
	mode uint32
}
type nativeOriginAncestryNode struct {
	file     *os.File
	identity nativeOriginAncestryID
}

// Copied primary state ONLY. No legacy mode, historical outcome, reservation,
// original creator custody, total native absence or deletion grant is encoded.
type nativeOriginAncestryView struct {
	state                     nativeOriginAncestryState
	missingComponent          string
	presentPrefix             []string
	disk, control, containers pinFileIdentity
	namespacePinsObserved     bool // always false in this ancestry-only reader
}

func (nativeOriginAncestryView) String() string     { return "native fixed ancestry [observation only]" }
func (v nativeOriginAncestryView) GoString() string { return v.String() }

type nativeOriginAncestry struct {
	mu                sync.Mutex
	diskPath          string // private fixture seam; production must derive trusted DiskMount
	uid, gid          int
	lifetime          context.Context // ORIGINAL callback execution; cannot be revived via WithoutCancel
	verifyCurrent     func(context.Context) error
	nodes             []nativeOriginAncestryNode // own original disk,runtime,openshell,containers
	state             nativeOriginAncestryState
	missingIndex      int // fixed component1..3 only; zero means all prefixes present
	complete, closed  bool
	failure, closeErr error
}

func (*nativeOriginAncestry) String() string     { return "native ancestry scope [not authority]" }
func (p *nativeOriginAncestry) GoString() string { return p.String() }

// Private callback-only fixture seam. No public caller/native admission is
// installed. Actual production caller must obtain DiskMount only from its
// trusted current layout/account-disk identity and verifyCurrent must perform
// original admitted UUID/ownership-marker/live-bind verification. This reader
// separately proves the fixed descendants have no symlink/mount shadow.
// It never creates a path, reads issuer material, or grants mutation/recovery.
// Every callback outcome expires the original handles and preserves Close errors.
func withNativeOriginAncestryAt(ctx context.Context, diskPath string, uid, gid int, verifyCurrent func(context.Context) error, use func(*nativeOriginAncestry) error) (err error) {
	if use == nil {
		return errors.New("native ancestry lacks a callback")
	}
	p, err := captureNativeOriginAncestryAt(ctx, diskPath, uid, gid, verifyCurrent)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, p.Close()) }()
	if err := use(p); err != nil {
		return err
	}
	return p.Check(ctx)
}

// Constructor is private primary observation, used only by callback wrapper or
// private fixtures. No storage verifier boolean is persisted as authority.
func captureNativeOriginAncestryAt(ctx context.Context, diskPath string, uid, gid int, verifyCurrent func(context.Context) error) (_ *nativeOriginAncestry, err error) {
	if ctx == nil || uid < 0 || gid < 0 || verifyCurrent == nil || !filepath.IsAbs(diskPath) || filepath.Clean(diskPath) != diskPath || diskPath == "/" {
		return nil, errors.New("native ancestry lacks trusted selected disk/current verifier")
	}
	lifetime := ctx
	ctx, cancel := context.WithTimeout(ctx, nativeOriginReadBudget)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := verifyCurrent(ctx); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p := &nativeOriginAncestry{diskPath: diskPath, uid: uid, gid: gid, lifetime: lifetime, verifyCurrent: verifyCurrent}
	defer func() {
		if err != nil {
			err = errors.Join(err, p.Close())
		}
	}()
	disk, err := openNativeOriginDiskRoot(diskPath)
	if err != nil {
		return nil, err
	}
	p.nodes = append(p.nodes, nativeOriginAncestryNode{file: disk})
	p.nodes[0].identity, err = nativeOriginAncestryDirectoryID(disk, uid, gid, false)
	if err != nil {
		return nil, err
	}
	for index := 1; index <= 3; index++ {
		name, _ := nativeOriginAncestryComponent(index)
		parent := p.nodes[index-1].file
		child, openErr := openNativeOriginFixedChild(parent, name)
		if openErr != nil {
			if !errors.Is(openErr, unix.ENOENT) {
				return nil, openErr
			}
			p.missingIndex = index
			switch index {
			case 1:
				p.state = nativeOriginAncestryMissingRuntime
			case 2:
				p.state = nativeOriginAncestryMissingControl
			case 3:
				p.state = nativeOriginAncestryMissingContainers
			}
			break // exact ENOENT is independently rechecked under retained parent
		}
		p.nodes = append(p.nodes, nativeOriginAncestryNode{file: child})
		p.nodes[index].identity, err = nativeOriginAncestryDirectoryID(child, uid, gid, true)
		if err != nil {
			return nil, err
		}
		rootID := p.nodes[0].identity.file
		id := p.nodes[index].identity.file
		if id.device != rootID.device || id.mount != rootID.mount {
			return nil, errors.New("native fixed descendant crosses selected disk mount")
		}
	}
	if p.missingIndex == 0 {
		p.state = nativeOriginAncestryPresent
	}
	if err := p.check(ctx); err != nil {
		return nil, err
	}
	p.complete = true
	return p, nil
}

func nativeOriginAncestryComponent(index int) (string, bool) {
	switch index {
	case 1:
		return "runtime", true
	case 2:
		return "openshell", true
	case 3:
		return "containers", true
	}
	return "", false
}

func openNativeOriginDiskRoot(path string) (*os.File, error) {
	fd, err := unix.Openat2(unix.AT_FDCWD, path, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS})
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), "native-selected-disk-root"), nil
}
func openNativeOriginFixedChild(parent *os.File, name string) (*os.File, error) {
	if parent == nil || name != "runtime" && name != "openshell" && name != "containers" {
		return nil, errors.New("invalid fixed native ancestry component")
	}
	fd, err := unix.Openat2(int(parent.Fd()), name, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_XDEV})
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), "native-fixed-ancestry"), nil
}

// The mounted ext4 disk root is not a per-container0700 leaf. Existing storage
// verification/preparation never chmods it to0700; a protected0755 root is valid.
// Fixed runtime/openshell/containers producers use PrivateDirectory0700.
func nativeOriginAncestryDirectoryID(file *os.File, uid, gid int, private bool) (nativeOriginAncestryID, error) {
	if file == nil || uid < 0 || gid < 0 {
		return nativeOriginAncestryID{}, errors.New("missing native ancestry directory")
	}
	var st unix.Stat_t
	var sx unix.Statx_t
	fd := int(file.Fd())
	if err := unix.Fstat(fd, &st); err != nil {
		return nativeOriginAncestryID{}, err
	}
	flags, err := unix.FcntlInt(file.Fd(), unix.F_GETFD, 0)
	if err != nil || flags&unix.FD_CLOEXEC == 0 {
		return nativeOriginAncestryID{}, errors.Join(errors.New("native ancestry descriptor lost close-on-exec"), err)
	}
	mode := st.Mode & 0o7777
	if st.Mode&unix.S_IFMT != unix.S_IFDIR || st.Uid != uint32(uid) || st.Gid != uint32(gid) || mode&0o7022 != 0 || private && mode != 0o700 {
		return nativeOriginAncestryID{}, errors.New("native ancestry has wrong directory type/owner/protection")
	}
	if err := unix.Statx(fd, "", unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW, unix.STATX_MNT_ID, &sx); err != nil {
		return nativeOriginAncestryID{}, err
	}
	if sx.Mask&unix.STATX_MNT_ID == 0 || sx.Mnt_id == 0 || st.Dev == 0 || st.Ino == 0 {
		return nativeOriginAncestryID{}, errors.New("native ancestry lacks mapped kernel object identity")
	}
	return nativeOriginAncestryID{file: pinFileIdentity{device: uint64(st.Dev), inode: st.Ino, mount: sx.Mnt_id}, mode: mode}, nil
}

func (p *nativeOriginAncestry) usable() error {
	if p == nil || p.closed || p.lifetime == nil || p.verifyCurrent == nil {
		return errors.New("native ancestry scope expired/closed/unavailable")
	}
	if err := p.lifetime.Err(); err != nil {
		return errors.Join(errors.New("native ancestry original execution expired"), err)
	}
	want := 4
	switch p.state {
	case nativeOriginAncestryMissingRuntime:
		if p.missingIndex != 1 {
			return errors.New("inconsistent missing-runtime state")
		}
		want = 1
	case nativeOriginAncestryMissingControl:
		if p.missingIndex != 2 {
			return errors.New("inconsistent missing-control state")
		}
		want = 2
	case nativeOriginAncestryMissingContainers:
		if p.missingIndex != 3 {
			return errors.New("inconsistent missing-containers state")
		}
		want = 3
	case nativeOriginAncestryPresent:
		if p.missingIndex != 0 {
			return errors.New("inconsistent present ancestry state")
		}
	default:
		return errors.New("uncaptured ancestry state")
	}
	if len(p.nodes) != want {
		return errors.New("native ancestry has partial original prefix coverage")
	}
	for _, node := range p.nodes {
		if node.file == nil {
			return errors.New("native ancestry lost original handle")
		}
	}
	return nil
}

func (p *nativeOriginAncestry) Check(ctx context.Context) error {
	if p == nil {
		return errors.New("nil native ancestry")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.complete || p.failure != nil {
		return errors.Join(errors.New("native ancestry incomplete/failed"), p.failure)
	}
	if ctx == nil {
		p.failure = errors.New("missing ancestry check context")
		return p.failure
	}
	ctx, cancel := context.WithTimeout(ctx, nativeOriginReadBudget)
	defer cancel()
	p.failure = p.check(ctx)
	return p.failure
}
func (p *nativeOriginAncestry) check(ctx context.Context) error {
	if err := p.usable(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := p.verifyCurrent(ctx); err != nil {
		return err
	}
	if err := p.checkNodes(ctx); err != nil {
		return err
	}
	if err := p.checkMissing(); err != nil {
		return err
	}
	if err := p.verifyCurrent(ctx); err != nil {
		return err
	}
	if err := p.checkNodes(ctx); err != nil {
		return err
	}
	if err := p.checkMissing(); err != nil {
		return err
	}
	if err := p.checkNodes(ctx); err != nil {
		return err
	} // bracket the final absent-child read
	if err := p.usable(); err != nil {
		return err
	}
	return ctx.Err()
}
func (p *nativeOriginAncestry) checkNodes(ctx context.Context) error {
	rootID := p.nodes[0].identity.file
	// Forward and reverse checks bracket child/absence work with each
	// original ancestor's current name; retained children alone are not enough.
	for pass := 0; pass < 2; pass++ {
		for offset := 0; offset < len(p.nodes); offset++ {
			index := offset
			if pass == 1 {
				index = len(p.nodes) - 1 - offset
			}
			node := p.nodes[index]
			if err := ctx.Err(); err != nil {
				return err
			}
			original, err := nativeOriginAncestryDirectoryID(node.file, p.uid, p.gid, index > 0)
			if err != nil || original != node.identity {
				return errors.Join(errors.New("native retained ancestry object changed"), err)
			}
			var named *os.File
			if index == 0 {
				named, err = openNativeOriginDiskRoot(p.diskPath)
			} else {
				name, _ := nativeOriginAncestryComponent(index)
				named, err = openNativeOriginFixedChild(p.nodes[index-1].file, name)
			}
			if err != nil {
				return err
			}
			current, identityErr := nativeOriginAncestryDirectoryID(named, p.uid, p.gid, index > 0)
			closeErr := named.Close()
			if identityErr != nil || closeErr != nil || current != node.identity || current.file.device != rootID.device || current.file.mount != rootID.mount {
				return errors.Join(errors.New("native named ancestry mapping/mount changed"), identityErr, closeErr)
			}
		}
	}
	return nil
}
func (p *nativeOriginAncestry) checkMissing() error {
	if p.missingIndex == 0 {
		return nil
	}
	name, valid := nativeOriginAncestryComponent(p.missingIndex)
	if !valid || len(p.nodes) != p.missingIndex {
		return errors.New("native absence lacks exact original fixed parent")
	}
	var st unix.Stat_t
	err := unix.Fstatat(int(p.nodes[len(p.nodes)-1].file.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	return errors.Join(errors.New("fixed native child is present or absence unavailable"), err)
}

func (p *nativeOriginAncestry) View() (nativeOriginAncestryView, error) {
	if p == nil {
		return nativeOriginAncestryView{}, errors.New("nil native ancestry")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.complete || p.failure != nil {
		return nativeOriginAncestryView{}, errors.Join(errors.New("native ancestry incomplete/failed"), p.failure)
	}
	if err := p.usable(); err != nil {
		return nativeOriginAncestryView{}, err
	}
	view := nativeOriginAncestryView{state: p.state, disk: p.nodes[0].identity.file, presentPrefix: []string{}, namespacePinsObserved: false}
	if p.missingIndex != 0 {
		view.missingComponent, _ = nativeOriginAncestryComponent(p.missingIndex)
	}
	for index := 1; index < len(p.nodes); index++ {
		name, _ := nativeOriginAncestryComponent(index)
		view.presentPrefix = append(view.presentPrefix, name)
	}
	if len(p.nodes) >= 3 {
		view.control = p.nodes[2].identity.file
	}
	if len(p.nodes) == 4 {
		view.containers = p.nodes[3].identity.file
	}
	return view, nil
}
func (p *nativeOriginAncestry) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return p.closeErr
	}
	p.closed, p.complete = true, false
	for index := len(p.nodes) - 1; index >= 0; index-- {
		if p.nodes[index].file != nil {
			p.closeErr = errors.Join(p.closeErr, p.nodes[index].file.Close())
			p.nodes[index].file = nil
		}
	}
	return p.closeErr
}
