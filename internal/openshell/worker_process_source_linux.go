//go:build linux

package openshell

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"github.com/leogr/yoloctl/internal/vmanchor"
	"github.com/leogr/yoloctl/internal/workercheckpoint"
	"golang.org/x/sys/unix"
)

// NativeWorkerSource is a BORROWED opaque attachment, NOT process/SDK admission.
// Its only constructor requires a concrete genuine handoff; zero objects, native
// Context/namespace/PID/path/copied metadata and callbacks cannot mint a source.
// Early CLI candidate retains token Close. This object never owns/closes token
// or its physical checkpoint channel and exposes no token/FD getter or installer.
type NativeWorkerSource struct {
	original          *vmanchor.WorkerHandoff // immutable SAME original token, never replace
	lifetime          context.Context
	contextOriginal   *spaceenv.Context
	contextSnapshot   spaceenv.Context // includes cloned Mounts, every field compared
	catalogSnapshot   string           // actual normal private catalog selection AFTER setup
	active            atomic.Bool
	attachmentFailure nativeWorkerAttachmentFailure // synchronized ORIGINAL loss only
}

func (*NativeWorkerSource) String() string {
	return "borrowed original worker source [attachment only]"
}
func (s *NativeWorkerSource) GoString() string { return s.String() }
func cloneWorkerNativeContext(c *spaceenv.Context) spaceenv.Context {
	if c == nil {
		return spaceenv.Context{}
	}
	copy := *c
	copy.Mounts = slices.Clone(c.Mounts)
	return copy
}

// Pure metadata equality only; equality itself supplies NO admission.
func sameWorkerNativeContext(original, current *spaceenv.Context, snapshot spaceenv.Context) bool {
	return original != nil && current == original && reflect.DeepEqual(cloneWorkerNativeContext(current), snapshot)
}
func workerSourceContextError(ctx context.Context) error {
	if ctx == nil {
		return errors.New("original worker context unavailable")
	}
	return errors.Join(ctx.Err(), context.Cause(ctx))
}
func workerSourceOutcome(err error) error {
	if errors.Is(err, workercheckpoint.ErrUncertain) {
		var already *NativeEffectsUncertain
		if !errors.As(err, &already) {
			return &NativeEffectsUncertain{Cause: err}
		}
	}
	return err
}

// nativeWorkerAttachmentFailure retains the FIRST observed original loss and
// any subsequently observed typed uncertainty. Its short mutex NEVER spans
// Context/token checks, callbacks, IO, gate waits or Close. Already-failed
// attachments cannot adopt restored current selection. Joining at most one new
// typed uncertainty avoids an unbounded tree from repeated duplicate failures.
type nativeWorkerAttachmentFailure struct {
	mu        sync.Mutex
	failure   error
	uncertain bool
}

func (f *nativeWorkerAttachmentFailure) load() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.failure
}
func (f *nativeWorkerAttachmentFailure) observe(originalErr error) error {
	hasUncertainty := errors.Is(originalErr, workercheckpoint.ErrUncertain)
	var nativeUncertain *NativeEffectsUncertain
	hasUncertainty = hasUncertainty || errors.As(originalErr, &nativeUncertain)
	f.mu.Lock()
	defer f.mu.Unlock()
	if originalErr != nil {
		if f.failure == nil {
			f.failure = originalErr
			f.uncertain = hasUncertainty
		} else if hasUncertainty && !f.uncertain {
			f.failure = errors.Join(f.failure, originalErr)
			f.uncertain = true
		}
	}
	return f.failure
}
func (s *NativeWorkerSource) recordOriginalAttachmentFailure(err error) error {
	if s == nil {
		return errors.Join(err, errors.New("original worker attachment missing"))
	}
	return s.attachmentFailure.observe(err)
}

// Pure primary metadata comparison used by the REAL check below. It cannot
// mint a source, configure Current, install native context or bypass token checks.
func (s *NativeWorkerSource) observeAttachmentSelection(current *spaceenv.Context, catalog string) error {
	if err := s.attachmentFailure.load(); err != nil {
		return err
	}
	if !sameWorkerNativeContext(s.contextOriginal, current, s.contextSnapshot) || catalog != s.catalogSnapshot {
		return s.recordOriginalAttachmentFailure(errors.New("ORIGINAL full worker native context/catalog changed"))
	}
	return s.attachmentFailure.load()
}
func (s *NativeWorkerSource) callerAttachmentResult(ctx context.Context) error {
	if s == nil {
		return errors.New("original worker attachment missing")
	}
	// A caller cancellation alone is NEVER stored as original identity loss.
	return errors.Join(s.attachmentFailure.load(), workerSourceContextError(ctx))
}
func (s *NativeWorkerSource) checkAttachment(ctx context.Context) error {
	if s == nil {
		return errors.New("original worker source attachment missing")
	}
	if err := s.attachmentFailure.load(); err != nil {
		return errors.Join(err, workerSourceContextError(ctx))
	}
	fail := func(err error) error {
		return errors.Join(s.recordOriginalAttachmentFailure(err), workerSourceContextError(ctx))
	}
	if !s.active.Load() || s.original == nil || s.lifetime == nil || s.contextOriginal == nil {
		return fail(errors.New("original worker source attachment expired/incomplete"))
	}
	if err := workerSourceContextError(s.lifetime); err != nil {
		return fail(errors.Join(errors.New("original worker attachment lifetime expired"), err))
	}
	if err := workerSourceContextError(ctx); err != nil {
		return err
	} // unrelated canceled caller: no original checks or latch
	if err := s.observeAttachmentSelection(spaceenv.Current(), spaceenv.CatalogDirectory()); err != nil {
		return errors.Join(err, workerSourceContextError(ctx))
	}
	if err := s.contextSnapshot.Validate(); err != nil {
		return fail(err)
	}
	if layout.ValidateExistingSpaceName(s.contextSnapshot.Space) != nil || s.contextSnapshot.DiskMount != layout.AccountDiskMount(s.contextSnapshot.DiskID) {
		return fail(errors.New("original worker native disk/layout mismatch"))
	}
	control, err := layout.OpenShellControlDir()
	if err != nil || control != filepath.Join(s.contextSnapshot.DiskMount, "runtime", "openshell") {
		return fail(errors.Join(errors.New("original worker fixed control layout changed"), err))
	}
	// Local token integrity only. NO opcode16/namespace duplicate/proc capture is
	// performed during attachment, which may precede BeginEffects.
	if err := s.original.Check(); err != nil {
		return fail(err)
	}
	if !s.active.Load() {
		return fail(errors.New("original worker source attachment expired during check"))
	}
	if err := workerSourceContextError(s.lifetime); err != nil {
		return fail(errors.Join(errors.New("original worker attachment lifetime expired"), err))
	}
	return s.callerAttachmentResult(ctx)
}

// Actual parent checking remains a closed genuine-token call. An independently
// observed original token failure is sticky even if caller cancellation also won.
// The parent-owned lower-layer successor MUST distinguish an untouched canceled
// channel waiter from admitted/closed-stream loss; this code NEVER clears w.failed
// or retries/replaces the parent source to repair the older token behavior.
func (s *NativeWorkerSource) checkOriginalParent(ctx context.Context, box string) error {
	if err := s.checkAttachment(ctx); err != nil {
		return err
	}
	err := s.original.CheckParent(ctx, box)
	if err == nil {
		return s.callerAttachmentResult(ctx)
	}
	originalErr := s.original.Check()
	if originalErr != nil {
		return errors.Join(s.recordOriginalAttachmentFailure(errors.Join(err, originalErr)), workerSourceContextError(ctx))
	}
	// With the required physical-owner distinction, this is the genuine token's
	// unchanged healthy state after a PRE-ADMISSION caller cancellation only.
	if ctx != nil && ctx.Err() != nil && errors.Is(err, ctx.Err()) {
		return errors.Join(s.attachmentFailure.load(), err, workerSourceContextError(ctx))
	}
	return s.recordOriginalAttachmentFailure(err)
}

// WithNativeWorkerSource attaches the actual concrete token only for this
// callback. Call AFTER ordinary runtime/private-catalog/context setup is stable,
// not during early intake. No context installer or actual caller is activated.
// Before-Begin attachment is permitted: it NEVER asks the effect-phase parent16.
// The actual WithProcessObservation call below requires its LIVE Box phase.
func WithNativeWorkerSource(ctx context.Context, original *vmanchor.WorkerHandoff, use func(*NativeWorkerSource) error) (result error) {
	defer func() { result = workerSourceOutcome(result) }()
	if ctx == nil || original == nil || use == nil {
		return errors.New("genuine original worker source callback unavailable")
	}
	if err := workerSourceContextError(ctx); err != nil {
		return err
	}
	// Reject zero/foreign/closed handoffs before consulting native Context; a
	// fake configured context alone cannot reach the borrowed callback.
	if err := original.Check(); err != nil {
		return err
	}
	current := spaceenv.Current()
	if current == nil || !spaceenv.Native() || os.Geteuid() != 0 || os.Getegid() != 0 || os.Getpid() == 1 {
		return errors.New("original worker native execution unavailable")
	}
	s := &NativeWorkerSource{original: original, lifetime: ctx, contextOriginal: current, contextSnapshot: cloneWorkerNativeContext(current), catalogSnapshot: spaceenv.CatalogDirectory()}
	s.active.Store(true)
	defer s.active.Store(false)
	if err := s.checkAttachment(ctx); err != nil {
		return err
	}
	useErr := use(s)
	return errors.Join(useErr, s.checkAttachment(ctx))
}

// NativeWorkerProcessRead is a closed, thread-bound callback borrow. No public
// process/namespace/FD/opener/dup/Close or positive classifier/absence API exists.
// Concrete SDK matchers may borrow the private scope after Check; they cannot
// replace its original source, proc root, namespace, kernel ops or universe.
type NativeWorkerProcessRead struct {
	scope  *nativeWorkerProcessScope
	active atomic.Bool
}

func (*NativeWorkerProcessRead) String() string {
	return "borrowed native process view [not classifier/absence ACK]"
}
func (r *NativeWorkerProcessRead) GoString() string { return r.String() }
func (r *NativeWorkerProcessRead) Check(ctx context.Context) error {
	if r == nil || !r.active.Load() || r.scope == nil {
		return errors.New("original process read expired/incomplete")
	}
	// Immutable TID checked before touching any mutable scope/observer state.
	// No cross-thread caller waits behind active checkpoint IO or closes it.
	if os.Getpid() != r.scope.pid || unix.Gettid() != r.scope.tid {
		return errors.New("original process read used by another PID/TID")
	}
	if r.scope.failure != nil {
		return workerSourceOutcome(r.scope.failure)
	}
	r.scope.failure = r.scope.check(ctx)
	return workerSourceOutcome(r.scope.failure)
}

type nativeWorkerProcessScope struct {
	source            *NativeWorkerSource
	box               string
	pid, tid          int      // immutable actual outer locked reader
	namespace, proc   *os.File // OWN independent checked dup and observer root only
	namespaceID       namespaceIdentity
	procID            pinFileIdentity
	universe          *nativeProcessUniverse // OWN actual retained complete process observer
	failure, closeErr error
	closed            bool
}

func (p *nativeWorkerProcessScope) close() error {
	if p == nil {
		return nil
	}
	if p.closed {
		return p.closeErr
	}
	p.closed = true
	if p.universe != nil {
		p.closeErr = errors.Join(p.closeErr, p.universe.Close())
	}
	for _, file := range []*os.File{p.proc, p.namespace} {
		if file != nil {
			p.closeErr = errors.Join(p.closeErr, file.Close())
		}
	}
	p.universe = nil
	p.proc = nil
	p.namespace = nil
	return p.closeErr // NEVER source.original.Close or physical checkpoint Close
}
func (p *nativeWorkerProcessScope) checkThread(ctx context.Context) error {
	if p == nil || p.closed || p.source == nil || p.pid != os.Getpid() || p.tid != unix.Gettid() {
		return errors.New("original process source closed/foreign reader")
	}
	return p.source.checkAttachment(ctx)
}
func (p *nativeWorkerProcessScope) checkNamespace() error {
	if p.namespace == nil || p.proc == nil {
		return errors.New("original independent namespace/proc observer unavailable")
	}
	flags, err := unix.FcntlInt(p.namespace.Fd(), unix.F_GETFD, 0)
	if err != nil || flags&unix.FD_CLOEXEC == 0 {
		return errors.Join(errors.New("owned original namespace lost CLOEXEC"), err)
	}
	id, err := nativeProcessPIDNamespaceIdentity(p.namespace)
	if err != nil || id != p.namespaceID {
		return errors.Join(errors.New("independent original PID namespace changed"), err)
	}
	procID, err := nativeProcessProcIdentity(p.proc, true)
	if err != nil || procID != p.procID {
		return errors.Join(errors.New("original genuine proc observer changed"), err)
	}
	// Intentional fixed thread-self under authenticated proc. This current
	// numeric view is compared TO the independently retained original handoff
	// duplicate; it is never itself treated as the VM-launch anchor.
	reader, err := nativeProcessOpenMagicReader(p.proc)
	if err != nil {
		return err
	}
	current, actual, openErr := openNativeProcessPIDNamespace(reader)
	closeErr := reader.Close()
	if current != nil {
		closeErr = errors.Join(closeErr, current.Close())
	}
	if openErr != nil || closeErr != nil || actual != p.namespaceID {
		return errors.Join(errors.New("active numerical process namespace differs from original handoff"), openErr, closeErr)
	}
	return nil
}
func (p *nativeWorkerProcessScope) capture(ctx context.Context) error {
	if err := p.checkThread(ctx); err != nil {
		return err
	}
	// Actual16 verifies SAME already-bound physical owner/original parent receipt,
	// requested Box write scope, active effects, source and CURRENT storage.
	// This is not an injected success callback or a copied Context proof.
	if err := p.source.checkOriginalParent(ctx, p.box); err != nil {
		return err
	}
	var err error
	p.namespace, err = p.source.original.DuplicatePIDNamespace()
	if err != nil {
		return err
	}
	p.namespaceID, err = nativeProcessPIDNamespaceIdentity(p.namespace)
	if err != nil {
		return err
	}
	fd, err := unix.Open("/proc", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	p.proc = os.NewFile(uintptr(fd), "original-worker-source-proc-observer")
	p.procID, err = nativeProcessProcIdentity(p.proc, true)
	if err != nil {
		return err
	}
	if err := p.checkNamespace(); err != nil {
		return err
	}
	// Only the CLOSED genuine source path supplies this independent comparison.
	// The default reader stays primary observation only. No invented ops field,
	// caller fixture ops, self/PID1 inference or missing-source empty callback.
	p.universe, err = readNativeProcessUniverseWithOps(ctx, p.proc, nativeProcessKernelOps())
	if err != nil {
		return err
	}
	if p.universe.view.readerPID != p.pid || p.universe.view.readerTID != p.tid || p.universe.view.readerPIDNamespace != p.namespaceID || p.universe.view.initPIDNamespace != p.namespaceID {
		return errors.New("complete observed process view differs from original handoff/locked reader")
	}
	return p.check(ctx)
}
func (p *nativeWorkerProcessScope) check(ctx context.Context) error {
	if err := p.checkThread(ctx); err != nil {
		return err
	}
	if p.universe == nil {
		return errors.New("complete original process observation unavailable")
	}
	// LAST external parent16/storage/receipt interval is BEFORE final complete
	// local process/namespace verification. Parent storage verification runs a
	// real child and must not come after the universe's final complete check.
	if err := p.source.checkOriginalParent(ctx, p.box); err != nil {
		return err
	}
	if err := p.checkNamespace(); err != nil {
		return err
	}
	if err := p.universe.Check(ctx); err != nil {
		return err
	}
	if err := p.checkNamespace(); err != nil {
		return err
	}
	return p.checkThread(ctx) // local token/context/thread only; NO parent16
}

// WithProcessObservation is actual SOURCE USE, permitted only AFTER successful
// BeginEffects and BEFORE completion/Release for this requested Box. Calling it
// too early is explicit unavailable proof; there is NO namespace/metadata or
// callback fallback. Generic pre-Begin source attachment above does not call16.
// One OUTER OS-thread lock spans original parent16, independent checked namespace,
// active numerical comparison, REAL full process capture, callback and rechecks.
// Nested locks in existing helpers cannot release this outer lock.
func (s *NativeWorkerSource) WithProcessObservation(ctx context.Context, box string, use func(*NativeWorkerProcessRead) error) (result error) {
	defer func() { result = workerSourceOutcome(result) }()
	if s == nil || ctx == nil || use == nil {
		return errors.New("original worker process source unavailable")
	}
	if err := layout.ValidateName("agentbox", box); err != nil {
		return err
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := s.checkAttachment(ctx); err != nil {
		return err
	}
	p := &nativeWorkerProcessScope{source: s, box: box, pid: os.Getpid(), tid: unix.Gettid()}
	defer func() { result = errors.Join(result, p.close()) }()
	if err := p.capture(ctx); err != nil {
		return err
	}
	read := &NativeWorkerProcessRead{scope: p}
	read.active.Store(true)
	defer read.active.Store(false) // expires BEFORE observer Close on every return
	if err := read.Check(ctx); err != nil {
		return err
	}
	useErr := use(read)
	// Run final check even when callback returned an error. Cancellation remains
	// explicit failed proof; it never substitutes a fabricated final observation.
	finalErr := read.Check(ctx)
	return errors.Join(useErr, finalErr)
}
