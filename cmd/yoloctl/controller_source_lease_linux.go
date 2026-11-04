//go:build linux

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"

	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"github.com/leogr/yoloctl/internal/vmanchor"
	"golang.org/x/sys/unix"
)

// All FDs except singletonFD are original OWNED observation handles. The lease
// NEVER closes/unlocks/duplicates the actual singleton FD or closes source.
type controllerLeaseTimestamp struct {
	Seconds     int64
	Nanoseconds uint32
}
type controllerLeaseFileID struct {
	Device, Inode, Mount uint64
	Mode, UID, GID       uint32
	Links                uint64
	Size                 int64
	Mtime, Ctime         controllerLeaseTimestamp
}

func controllerLeaseID(fd int, name string) (controllerLeaseFileID, error) {
	var x unix.Statx_t
	flags := unix.AT_SYMLINK_NOFOLLOW
	if name == "" {
		flags |= unix.AT_EMPTY_PATH
	}
	want := uint32(unix.STATX_BASIC_STATS | unix.STATX_MNT_ID)
	if err := unix.Statx(fd, name, flags, int(want), &x); err != nil {
		return controllerLeaseFileID{}, err
	}
	if x.Mask&want != want || x.Mnt_id == 0 || x.Size > uint64(1<<63-1) {
		return controllerLeaseFileID{}, errors.New("original controller observer lacks complete mount/file identity")
	}
	// One statx snapshot supplies all fields; do not mix two inode observations.
	return controllerLeaseFileID{unix.Mkdev(x.Dev_major, x.Dev_minor), x.Ino, x.Mnt_id, uint32(x.Mode), x.Uid, x.Gid, uint64(x.Nlink), int64(x.Size), controllerLeaseTimestamp{x.Mtime.Sec, x.Mtime.Nsec}, controllerLeaseTimestamp{x.Ctime.Sec, x.Ctime.Nsec}}, nil
}
func (a controllerLeaseFileID) sameDirectory(b controllerLeaseFileID) bool {
	// Directory member bookkeeping changes during ordinary receipts/listen.
	return a.Device == b.Device && a.Inode == b.Inode && a.Mount == b.Mount && a.Mode == b.Mode && a.UID == b.UID && a.GID == b.GID
}
func controllerLeaseProtected(id controllerLeaseFileID, kind uint32) bool {
	return id.Mode&unix.S_IFMT == kind && id.UID == 0 && id.Mode&0o022 == 0
}
func controllerLeaseRead(file *os.File) ([]byte, error) {
	raw, err := io.ReadAll(io.NewSectionReader(file, 0, 4097))
	if err != nil {
		return nil, err
	}
	if len(raw) > 4096 {
		return nil, errors.New("original activation exceeds existing bound")
	}
	return raw, nil
}

type controllerLeasePathNode struct {
	file *os.File
	name string
	id   controllerLeaseFileID
}
type controllerLeasePath struct {
	nodes  []controllerLeasePathNode
	closed bool
}

// Every ancestor is pinned independently with metadata-only O_PATH handles.
// No directory listing/read grant is required. Mount crossings remain supported;
// each retained mapping's own mount identity must remain the original one.
func captureControllerLeasePath(path string) (p *controllerLeasePath, err error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("noncanonical fixed controller observer path")
	}
	p = &controllerLeasePath{}
	owned := p
	defer func() {
		if err != nil {
			err = errors.Join(err, owned.Close())
		}
	}()
	fd, err := unix.Open("/", unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	root := os.NewFile(uintptr(fd), "controller-observer-root")
	p.nodes = append(p.nodes, controllerLeasePathNode{file: root})
	p.nodes[0].id, err = controllerLeaseID(fd, "")
	if err != nil {
		return nil, err
	}
	if !controllerLeaseProtected(p.nodes[0].id, unix.S_IFDIR) {
		return nil, errors.New("controller observer root is unprotected")
	}
	for _, name := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if name == "" {
			continue
		}
		parent := p.nodes[len(p.nodes)-1].file
		fd, openErr := unix.Openat(int(parent.Fd()), name, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if openErr != nil {
			return nil, openErr
		}
		child := os.NewFile(uintptr(fd), "controller-observer-directory")
		p.nodes = append(p.nodes, controllerLeasePathNode{file: child, name: name})
		id, idErr := controllerLeaseID(fd, "")
		if idErr != nil {
			return nil, idErr
		}
		p.nodes[len(p.nodes)-1].id = id
		if !controllerLeaseProtected(id, unix.S_IFDIR) {
			return nil, errors.New("controller observer ancestry is unprotected")
		}
	}
	if err = p.Check(); err != nil {
		return nil, err
	}
	return p, nil
}
func (p *controllerLeasePath) leaf() *os.File {
	if p == nil || len(p.nodes) == 0 {
		return nil
	}
	return p.nodes[len(p.nodes)-1].file
}
func (p *controllerLeasePath) Check() error {
	if p == nil || p.closed || len(p.nodes) == 0 {
		return errors.New("original controller path observer unavailable")
	}
	// Root's current mapping is verified, not reconstructed as replacement.
	fd, err := unix.Open("/", unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	now, rootErr := controllerLeaseID(fd, "")
	closeErr := unix.Close(fd)
	if rootErr != nil || closeErr != nil || !p.nodes[0].id.sameDirectory(now) {
		return errors.Join(rootErr, closeErr, errors.New("original controller root mapping changed"))
	}
	for i, node := range p.nodes {
		own, err := controllerLeaseID(int(node.file.Fd()), "")
		if err != nil || !node.id.sameDirectory(own) {
			return errors.Join(err, errors.New("original controller directory changed"))
		}
		if i > 0 {
			named, err := controllerLeaseID(int(p.nodes[i-1].file.Fd()), node.name)
			if err != nil || !node.id.sameDirectory(named) {
				return errors.Join(err, errors.New("original controller directory mapping changed"))
			}
		}
	}
	return nil
}
func (p *controllerLeasePath) Close() error {
	if p == nil || p.closed {
		return nil
	}
	p.closed = true
	var errs []error
	for i := len(p.nodes) - 1; i >= 0; i-- {
		if p.nodes[i].file != nil {
			errs = append(errs, p.nodes[i].file.Close())
		}
	}
	return errors.Join(errs...)
}

type controllerActivationPin struct {
	mu                 sync.Mutex
	parent, file       *os.File // FIRST actual activation read; never reopened/adopted
	parentID, id       controllerLeaseFileID
	raw                []byte
	ancestry           *controllerLeasePath
	proofErr, closeErr error
	closed             bool
}

// Replaces only the FIRST existing safepath activation read in serveController.
// A read error retains existing startup refusal. Extra observer qualification
// errors are recorded separately, so unsupported optional proof adds no startup
// refusal. File/parent are retained from this FIRST read even in that case.
func readOriginalControllerActivation(meta string) (_ controllerActivationObservation, err error) {
	parentFD, err := unix.Open(meta, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	p := &controllerActivationPin{parent: os.NewFile(uintptr(parentFD), "controller-first-activation-parent")}
	defer func() {
		if err != nil {
			err = errors.Join(err, p.Close())
		}
	}()
	fileFD, err := unix.Openat(parentFD, "activated", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	p.file = os.NewFile(uintptr(fileFD), "controller-first-activation")
	var st unix.Stat_t
	if err = unix.Fstat(fileFD, &st); err != nil {
		return nil, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return nil, errors.New("activation is not a regular file")
	}
	before, idErr := controllerLeaseID(fileFD, "")
	p.raw, err = controllerLeaseRead(p.file)
	if err != nil {
		return nil, err
	}
	after, afterErr := controllerLeaseID(fileFD, "")
	p.parentID, p.proofErr = controllerLeaseID(parentFD, "")
	p.id = before
	p.proofErr = errors.Join(p.proofErr, idErr, afterErr)
	if idErr == nil && afterErr == nil && before != after {
		p.proofErr = errors.Join(p.proofErr, errors.New("activation changed during FIRST read"))
	}
	if p.proofErr == nil {
		// Actual ActivateStore allows whitespace around a 32-byte hex digest and
		// copies ready bytes verbatim at0600; do not invent canonical0444 input.
		decoded, e := hex.DecodeString(strings.TrimSpace(string(p.raw)))
		if e != nil || len(decoded) != sha256.Size || !controllerLeaseProtected(p.id, unix.S_IFREG) || p.id.Mode&0o077 != 0 || p.id.Links != 1 || !controllerLeaseProtected(p.parentID, unix.S_IFDIR) || p.parentID.Mode&0o077 != 0 {
			p.proofErr = errors.New("activation observer lacks original protected installed checkpoint")
		}
	}
	if p.proofErr == nil {
		p.ancestry, p.proofErr = captureControllerLeasePath(meta)
		if p.proofErr == nil {
			mapped, e := controllerLeaseID(int(p.ancestry.leaf().Fd()), "")
			if e != nil || !p.parentID.sameDirectory(mapped) {
				p.proofErr = errors.Join(e, errors.New("FIRST activation parent no longer mapped"))
			}
		}
	}
	if p.proofErr == nil {
		p.proofErr = p.checkLocked()
	}
	return p, nil
}
func (p *controllerActivationPin) checkLocked() error {
	if p == nil {
		return errors.New("FIRST activation observer missing")
	}
	if p.closed || p.proofErr != nil {
		return errors.Join(errors.New("FIRST activation observer unavailable"), p.proofErr)
	}
	return p.checkMappingsLocked()
}

// Local object/mapping observation only; the qualified path above must first
// retain its original protection premise. This helper cannot mint a VM source.
func (p *controllerActivationPin) checkMappingsLocked() error {
	if p == nil || p.closed || p.parent == nil || p.file == nil || p.ancestry == nil {
		return errors.New("original activation mappings unavailable")
	}
	if err := p.ancestry.Check(); err != nil {
		return err
	}
	parent, err := controllerLeaseID(int(p.parent.Fd()), "")
	if err != nil || !p.parentID.sameDirectory(parent) {
		return errors.Join(err, errors.New("original activation parent changed"))
	}
	mapped, err := controllerLeaseID(int(p.ancestry.leaf().Fd()), "")
	if err != nil || !p.parentID.sameDirectory(mapped) {
		return errors.Join(err, errors.New("original activation parent mapping changed"))
	}
	own, err := controllerLeaseID(int(p.file.Fd()), "")
	if err != nil || own != p.id {
		return errors.Join(err, errors.New("original activation file changed"))
	}
	named, err := controllerLeaseID(int(p.parent.Fd()), "activated")
	if err != nil || named != p.id {
		return errors.Join(err, errors.New("original activation name replaced"))
	}
	raw, err := controllerLeaseRead(p.file)
	if err != nil || !bytes.Equal(raw, p.raw) {
		return errors.Join(err, errors.New("original activation bytes changed"))
	}
	after, err := controllerLeaseID(int(p.file.Fd()), "")
	if err != nil || after != p.id {
		return errors.Join(err, errors.New("original activation changed during check"))
	}
	named, err = controllerLeaseID(int(p.parent.Fd()), "activated")
	if err != nil || named != p.id {
		return errors.Join(err, errors.New("original activation final name mapping changed"))
	}
	return p.ancestry.Check()
}
func (p *controllerActivationPin) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return p.closeErr
	}
	p.closed = true
	var errs []error
	if p.file != nil {
		errs = append(errs, p.file.Close())
	}
	if p.parent != nil {
		errs = append(errs, p.parent.Close())
	}
	if p.ancestry != nil {
		errs = append(errs, p.ancestry.Close())
	}
	p.closeErr = errors.Join(errs...)
	return p.closeErr
}

type controllerWorkerSourceLease struct {
	admission                            controllerSourceAdmission
	lifetime                             context.Context
	originalContext                      *spaceenv.Context
	copiedContext                        spaceenv.Context
	candidate                            *controllerLaunchCandidate
	source                               *vmanchor.LaunchAnchor   // BORROWED exact original; candidate owns Close
	activation                           *controllerActivationPin // BORROWED FIRST observer; serve owns Close
	singletonFD                          int                      // BORROWED already-flocked original, never dup/unlock/Close
	singletonID                          controllerLeaseFileID
	runPath                              *controllerLeasePath // OWNED mapping observer only
	boot, fingerprint, sourceAssociation string
	failure, closeErr                    error
	closed                               bool
}

func copiedControllerContext(c *spaceenv.Context) spaceenv.Context {
	copy := *c
	copy.Mounts = slices.Clone(c.Mounts)
	return copy
}

// CLI-private factory only at actual post-Flock/first-activation serve call site.
// It consumes NO source identity/Hello path input and does not assign Server.
func captureControllerServiceSource(ctx context.Context, c *spaceenv.Context, singletonFD int, observation controllerActivationObservation) (_ controllerServiceSource, err error) {
	if ctx == nil || c == nil || spaceenv.Current() != c || c.Validate() != nil || singletonFD < 0 {
		return nil, errors.New("original controller native context unavailable")
	}
	activation, ok := observation.(*controllerActivationPin)
	if !ok || activation == nil {
		return nil, errors.New("FIRST original activation observation unavailable")
	}
	candidate, ok := ctx.Value(controllerLaunchCandidateKey{}).(*controllerLaunchCandidate)
	if !ok || candidate == nil {
		return nil, errors.New("original early controller source unavailable")
	}
	l := &controllerWorkerSourceLease{lifetime: ctx, originalContext: c, copiedContext: copiedControllerContext(c), candidate: candidate, activation: activation, singletonFD: singletonFD}
	defer func() {
		if err != nil {
			err = errors.Join(err, l.Close())
		}
	}()
	waitCtx, cancelWait := context.WithTimeout(ctx, controllerSourceAdmissionWait)
	defer cancelWait()
	release, waitErr := l.admission.acquire(waitCtx, l.lifetime)
	if waitErr != nil {
		return nil, waitErr
	}
	defer release()
	unlockCandidate, waitErr := controllerSourceLock(waitCtx, l.lifetime, l.admission.stopping, &candidate.mu)
	if waitErr != nil {
		return nil, waitErr
	}
	defer unlockCandidate()
	if candidate.closed || candidate.failure != nil {
		return nil, errors.Join(candidate.failure, errors.New("original early source lost"))
	}
	l.source, ok = candidate.source.(*vmanchor.LaunchAnchor)
	if !ok || l.source == nil {
		return nil, errors.New("actual FC original source capability unavailable")
	}
	unlockActivation, waitErr := controllerSourceLock(waitCtx, l.lifetime, l.admission.stopping, &activation.mu)
	if waitErr != nil {
		return nil, waitErr
	}
	defer unlockActivation()
	cancelWait() // only QUEUE time was bounded; original caller/lifetime stay live
	if err = controllerSourceContextError(ctx); err != nil {
		return nil, err
	}
	l.singletonID, err = controllerLeaseID(singletonFD, "")
	if err != nil {
		return nil, err
	}
	if !controllerLeaseProtected(l.singletonID, unix.S_IFREG) || l.singletonID.Mode&0o077 != 0 || l.singletonID.Links != 1 {
		return nil, errors.New("original singleton file is unprotected")
	}
	l.runPath, err = captureControllerLeasePath(spaceenv.RunRoot)
	if err != nil {
		return nil, err
	}
	l.boot, l.fingerprint, err = l.source.ObservationIdentity()
	if err != nil {
		return nil, err
	}
	if err = activation.checkLocked(); err != nil {
		return nil, err
	}
	rawHash := sha256.Sum256(activation.raw)
	association, err := json.Marshal(struct {
		Version          int                   `json:"version"`
		Boot             string                `json:"boot"`
		Fingerprint      string                `json:"fingerprint"`
		Activation       controllerLeaseFileID `json:"activation"`
		ActivationSHA256 string                `json:"activation_sha256"`
	}{1, l.boot, l.fingerprint, activation.id, hex.EncodeToString(rawHash[:])})
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(association)
	l.sourceAssociation = "fc-controller-lease-v1:" + hex.EncodeToString(sum[:])
	if err = l.verifyStorageAndOriginalLocked(); err != nil {
		return nil, err
	}
	if err = controllerSourceContextError(ctx); err != nil {
		return nil, err
	}
	return l, nil
}

// candidate.mu and activation.mu held. External readonly storage verification
// precedes final callback-free original mapping/content/source checks.
func (l *controllerWorkerSourceLease) verifyStorageAndOriginalLocked() error {
	if err := l.checkOriginalLocked(); err != nil {
		return err
	}
	if err := controller.VerifyRuntimeStorage(l.lifetime); err != nil {
		return err
	}
	return l.checkOriginalLocked()
}
func (l *controllerWorkerSourceLease) checkContextOriginalLocked() error {
	if l == nil || l.lifetime == nil {
		return errors.New("original controller lease unavailable")
	}
	if err := controllerSourceContextError(l.lifetime); err != nil {
		return errors.Join(errors.New("original controller lifetime expired"), err)
	}
	if l.closed || l.originalContext == nil || l.candidate == nil || spaceenv.Current() != l.originalContext || !reflect.DeepEqual(copiedControllerContext(l.originalContext), l.copiedContext) || l.candidate.closed || l.candidate.failure != nil || l.candidate.source != l.source {
		return errors.New("original controller lease/context/source expired/replaced")
	}
	return nil
}
func (l *controllerWorkerSourceLease) checkOriginalLocked() error {
	if err := l.checkContextOriginalLocked(); err != nil {
		return err
	}
	if err := l.source.Check(); err != nil {
		return err
	}
	boot, fingerprint, err := l.source.ObservationIdentity()
	if err != nil || boot != l.boot || fingerprint != l.fingerprint {
		return errors.Join(err, errors.New("original source observation changed"))
	}
	if err := l.activation.checkLocked(); err != nil {
		return err
	}
	if err := l.runPath.Check(); err != nil {
		return err
	}
	return errors.Join(checkControllerSingletonMappings(l.singletonFD, l.singletonID, l.runPath), l.checkContextOriginalLocked())
}
func checkControllerSingletonMappings(fd int, id controllerLeaseFileID, path *controllerLeasePath) error {
	if path == nil {
		return errors.New("original singleton path unavailable")
	}
	if err := path.Check(); err != nil {
		return err
	}
	own, err := controllerLeaseID(fd, "")
	if err != nil || own != id {
		return errors.Join(err, errors.New("original held singleton descriptor changed"))
	}
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	if err != nil || flags&unix.FD_CLOEXEC == 0 {
		return errors.Join(err, errors.New("original held singleton inheritance changed"))
	}
	access, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err != nil || access&unix.O_ACCMODE != unix.O_RDWR {
		return errors.Join(err, errors.New("original singleton access changed"))
	}
	named, err := controllerLeaseID(int(path.leaf().Fd()), "service.lock")
	if err != nil || named != id {
		return errors.Join(err, errors.New("original singleton mapping replaced"))
	}
	return nil
}
func (l *controllerWorkerSourceLease) WithWorkerLaunchSource(ctx context.Context, use func(controller.WorkerLaunchSource, controller.WorkerControllerAssociation) error) (err error) {
	if l == nil || use == nil || ctx == nil {
		return errors.New("original controller source borrow unavailable")
	}
	waitCtx, cancelWait := context.WithTimeout(ctx, controllerSourceAdmissionWait)
	defer cancelWait()
	release, err := l.admission.acquire(waitCtx, l.lifetime)
	if err != nil {
		return err
	}
	defer release()
	if l.closed || l.failure != nil {
		return errors.Join(l.failure, errors.New("original controller source lease closed/lost"))
	}
	if l.candidate == nil || l.activation == nil || l.source == nil || l.runPath == nil {
		l.failure = errors.New("original controller source lease incomplete")
		return l.failure
	}
	unlockCandidate, err := controllerSourceLock(waitCtx, l.lifetime, l.admission.stopping, &l.candidate.mu)
	if err != nil {
		return err
	}
	defer unlockCandidate()
	unlockActivation, err := controllerSourceLock(waitCtx, l.lifetime, l.admission.stopping, &l.activation.mu)
	if err != nil {
		return err
	}
	defer unlockActivation()
	cancelWait() // never reuse the artificially expired QUEUE context for I/O
	if err = controllerSourceContextError(ctx); err != nil {
		return err
	}
	// Active original storage checks use SAME service lifetime with its existing5s
	// verifier bound. A single worker's cancellation cannot corrupt global source.
	if err = l.verifyStorageAndOriginalLocked(); err != nil {
		l.failure = err
		return errors.Join(err, controllerSourceContextError(ctx))
	}
	association := controller.WorkerControllerAssociation{Version: 1, Hello: controller.Hello{Protocol: controller.ProtocolVersion, Bundle: l.copiedContext.Bundle, Space: l.copiedContext.Space, Generation: l.copiedContext.Generation}, DiskID: l.copiedContext.DiskID, Context: copiedControllerContext(&l.copiedContext), SourceAssociation: l.sourceAssociation, Singleton: controller.WorkerSingletonAssociation{Descriptor: l.singletonFD, Device: l.singletonID.Device, Inode: l.singletonID.Inode, Mount: l.singletonID.Mount}}
	useErr := controllerSourceContextError(ctx)
	if useErr == nil {
		useErr = use(l.source, association)
	}
	// Mandatory final original checks still run after callback cancellation/error.
	// Poison global lease only on original source/storage/lifetime/mapping loss.
	finalErr := l.verifyStorageAndOriginalLocked()
	if finalErr != nil {
		l.failure = finalErr
	}
	return errors.Join(useErr, finalErr, controllerSourceContextError(ctx))
}
func (l *controllerWorkerSourceLease) CheckContext(ctx context.Context) error {
	return l.WithWorkerLaunchSource(ctx, func(controller.WorkerLaunchSource, controller.WorkerControllerAssociation) error { return nil })
}
func (l *controllerWorkerSourceLease) Check() error {
	if l == nil {
		return errors.New("original source lease missing")
	}
	return l.CheckContext(l.lifetime)
}
func (l *controllerWorkerSourceLease) DuplicatePIDNamespace() (duplicate *os.File, err error) {
	if l == nil {
		return nil, errors.New("original source lease missing")
	}
	err = l.WithWorkerLaunchSource(l.lifetime, func(source controller.WorkerLaunchSource, _ controller.WorkerControllerAssociation) error {
		var e error
		duplicate, e = source.DuplicatePIDNamespace()
		return e
	})
	if err != nil && duplicate != nil {
		err = errors.Join(err, duplicate.Close())
		duplicate = nil
	}
	return duplicate, err
}
func (l *controllerWorkerSourceLease) Close() error {
	if l == nil {
		return nil
	}
	return l.admission.closeOwned(func() error {
		l.closed = true // exclusive: actual active borrower has settled, new ones refuse
		if l.runPath != nil {
			l.closeErr = l.runPath.Close()
		}
		return l.closeErr // OWN observers ONLY, never activation/singleton/source
	})
}
