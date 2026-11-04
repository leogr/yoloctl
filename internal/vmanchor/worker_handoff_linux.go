//go:build linux

package vmanchor

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/leogr/yoloctl/internal/workercheckpoint"

	"golang.org/x/sys/unix"
)

// WorkerHandoffRequest is private correlation bound to an actual RunWorker
// input. AssociationDigest is SHA256 of the original admitted owner/receipt/
// controller/storage association, computed by that owner, not from a reply.
// InputDigest is SHA256 of the exact canonical private WorkerInput bytes (no
// newline). It is computed separately, NOT serialized into its own input.
// These values carry no admission/receipt authority independently of the SAME
// original source and checkpoint owner in the reviewed production call graph.
// Version 1 keeps payload3/checkpoint4, view5 if present, phase FIRST, then
// namespace followed immediately by bootstrap; no public argv/env transport.
type WorkerHandoffRequest struct {
	ExecutionID       string `json:"execution_id"`
	Method            string `json:"method"`
	AssociationDigest string `json:"association_digest"`
	InputDigest       string `json:"input_digest"`
	ViewFD            int    `json:"view_fd"`
	PhaseFD           int    `json:"phase_fd"`
	NamespaceFD       int    `json:"namespace_fd"`
	BootstrapFD       int    `json:"bootstrap_fd"`
}

// WorkerHandoffFiles are BORROWED original child-side mandatory/optional
// descriptors. This API never closes or wraps them; existing RunWorker owns
// their framing/Close. Namespace/bootstrap duplicates are owned separately.
type WorkerHandoffFiles struct{ Payload, Checkpoint, View, Phase *os.File }

type workerDescriptor struct {
	FD     int    `json:"fd"`
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
	Mode   uint32 `json:"mode"`
}

type workerDescriptors struct {
	Payload    workerDescriptor `json:"payload"`
	Checkpoint workerDescriptor `json:"checkpoint"`
	View       workerDescriptor `json:"view"`
	Phase      workerDescriptor `json:"phase"`
	Namespace  workerDescriptor `json:"namespace"`
	Bootstrap  workerDescriptor `json:"bootstrap"`
}

type workerHandoffFrame struct {
	Version          int                  `json:"version"`
	Transfer         string               `json:"transfer"`
	Boot             string               `json:"boot"`
	ControllerLaunch string               `json:"controller_launch"`
	Controller       processIdentity      `json:"controller"`
	Worker           processIdentity      `json:"worker"`
	Request          WorkerHandoffRequest `json:"request"`
	Descriptors      workerDescriptors    `json:"descriptors"`
}

func canonicalHex(value string, n int) bool {
	raw, err := hex.DecodeString(value)
	return err == nil && len(raw) == n && hex.EncodeToString(raw) == value
}

func (r WorkerHandoffRequest) validate() error {
	if len(r.ExecutionID) == 0 || len(r.ExecutionID) > 128 || len(r.Method) == 0 || len(r.Method) > 128 || !canonicalHex(r.AssociationDigest, 32) || !canonicalHex(r.InputDigest, 32) {
		return errors.New("invalid original worker request binding")
	}
	next := 5
	if r.ViewFD != 0 {
		if r.ViewFD != next {
			return errors.New("invalid private worker view slot")
		}
		next++
	}
	if r.PhaseFD != 0 {
		if r.PhaseFD != next {
			return errors.New("phase must precede worker namespace")
		}
		next++
	}
	if r.NamespaceFD != next || r.BootstrapFD != next+1 {
		return errors.New("invalid private worker namespace/bootstrap slots")
	}
	return nil
}

func rawWorkerDescriptor(fd, slot int, kind uint32) (workerDescriptor, error) {
	var st unix.Stat_t
	if fd < 0 || unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != kind || st.Dev == 0 || st.Ino == 0 {
		return workerDescriptor{}, errors.New("wrong private worker descriptor type or identity")
	}
	return workerDescriptor{slot, uint64(st.Dev), st.Ino, st.Mode & unix.S_IFMT}, nil
}

func workerSocket(fd, typ int) error {
	domain, e := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_DOMAIN)
	if e != nil || domain != unix.AF_UNIX {
		return errors.New("worker channel is not local unix socket")
	}
	got, e := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_TYPE)
	if e != nil || got != typ {
		return errors.New("wrong private worker socket type")
	}
	return nil
}

func workerFile(fd, slot int, kind uint32, typ int) (workerDescriptor, error) {
	id, e := rawWorkerDescriptor(fd, slot, kind)
	if e == nil && typ != 0 {
		e = workerSocket(fd, typ)
	}
	if e == nil && slot == 3 {
		flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
		if err != nil || flags&unix.O_ACCMODE != unix.O_RDONLY {
			e = errors.New("worker payload must be original read pipe")
		}
	}
	return id, e
}

func workerFileDescriptor(file *os.File, slot int, kind uint32, typ int) (workerDescriptor, error) {
	if file == nil {
		return workerDescriptor{}, errors.New("missing original worker descriptor")
	}
	return workerFile(int(file.Fd()), slot, kind, typ)
}

func producerWorkerDescriptors(r WorkerHandoffRequest, f WorkerHandoffFiles, namespace, bootstrap *os.File) (d workerDescriptors, err error) {
	for _, v := range []struct {
		file *os.File
		slot int
		kind uint32
		typ  int
		out  *workerDescriptor
	}{
		{f.Payload, 3, unix.S_IFIFO, 0, &d.Payload}, {f.Checkpoint, 4, unix.S_IFSOCK, unix.SOCK_STREAM, &d.Checkpoint},
		{f.View, r.ViewFD, unix.S_IFSOCK, unix.SOCK_STREAM, &d.View}, {f.Phase, r.PhaseFD, unix.S_IFIFO, 0, &d.Phase},
		{namespace, r.NamespaceFD, unix.S_IFREG, 0, &d.Namespace}, {bootstrap, r.BootstrapFD, unix.S_IFSOCK, unix.SOCK_SEQPACKET, &d.Bootstrap},
	} {
		if v.slot == 0 {
			if v.file != nil {
				return d, errors.New("unexpected optional worker descriptor")
			}
			continue
		}
		*v.out, err = workerFileDescriptor(v.file, v.slot, v.kind, v.typ)
		if err != nil {
			return d, err
		}
	}
	return d, nil
}

func consumerWorkerDescriptors(r WorkerHandoffRequest) (d workerDescriptors, err error) {
	for _, v := range []struct {
		slot int
		kind uint32
		typ  int
		out  *workerDescriptor
	}{
		{3, unix.S_IFIFO, 0, &d.Payload}, {4, unix.S_IFSOCK, unix.SOCK_STREAM, &d.Checkpoint},
		{r.ViewFD, unix.S_IFSOCK, unix.SOCK_STREAM, &d.View}, {r.PhaseFD, unix.S_IFIFO, 0, &d.Phase},
		{r.NamespaceFD, unix.S_IFREG, 0, &d.Namespace}, {r.BootstrapFD, unix.S_IFSOCK, unix.SOCK_SEQPACKET, &d.Bootstrap},
	} {
		if v.slot == 0 {
			continue
		}
		*v.out, err = workerFile(v.slot, v.slot, v.kind, v.typ)
		if err != nil {
			return d, err
		}
	}
	for _, fd := range []int{3, 4} {
		flags, e := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
		if e != nil || flags&unix.FD_CLOEXEC == 0 {
			return d, errors.New("mandatory worker handles must be claimed CLOEXEC before optional intake")
		}
	}
	return d, nil
}

// WorkerHandoffTransfer borrows the SAME original anchor; only that anchor's
// controller lifetime owns Close. The transfer owns its two child duplicates,
// sender, and exact started-worker pin, retained for the original checkpoint
// closure's Check. No method starts/waits/kills a process or repairs a source.
type WorkerHandoffTransfer struct {
	mu                           sync.Mutex
	source                       *LaunchAnchor
	request                      WorkerHandoffRequest
	descriptors                  workerDescriptors
	namespace, child, parent     *os.File
	worker                       *processPin
	transfer                     string
	attempted, published, closed bool
	failed                       error
}

// PrepareWorkerHandoff must be invoked by the SAME admitted original owner in
// actual RunWorker while its lifetime borrow is held. Request strings/digests
// do not mint a producer: the original LaunchAnchor and production owner join
// are mandatory. Source-unavailable is optional, never an ordinary Start error.
func (a *LaunchAnchor) PrepareWorkerHandoff(request WorkerHandoffRequest, files WorkerHandoffFiles) (t *WorkerHandoffTransfer, err error) {
	if err = request.validate(); err != nil {
		return nil, err
	}
	if a == nil {
		return nil, errors.New("original controller anchor unavailable")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	a.mu.Lock()
	defer a.mu.Unlock()
	if err = a.check(); err != nil {
		return nil, err
	}
	t = &WorkerHandoffTransfer{source: a, request: request}
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
	t.transfer = hex.EncodeToString(nonce[:])
	t.namespace, err = duplicate(a.namespace)
	if err != nil {
		return t, err
	}
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_NONBLOCK|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return t, err
	}
	t.parent, t.child = os.NewFile(uintptr(pair[0]), "worker-handoff-parent"), os.NewFile(uintptr(pair[1]), "worker-handoff-child")
	if err = unix.SetsockoptInt(pair[1], unix.SOL_SOCKET, unix.SO_PASSCRED, 1); err != nil {
		return t, err
	}
	t.descriptors, err = producerWorkerDescriptors(request, files, t.namespace, t.child)
	if err != nil {
		return t, err
	}
	err = a.check()
	return t, err
}

// ChildFiles appends only namespace, then bootstrap; phase is already first.
func (t *WorkerHandoffTransfer) ChildFiles() []*os.File {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed || t.attempted {
		return nil
	}
	return []*os.File{t.namespace, t.child}
}

// PublishStarted performs ONE bounded nonblocking publication after actual
// successful Start. Any error means unavailable proof; caller MUST keep the
// actual child/sole Wait/status and never replay Start or a checkpoint.
func (t *WorkerHandoffTransfer) PublishStarted(child *os.Process) error {
	if t == nil {
		return errors.New("missing original worker transfer")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed || t.attempted {
		return errors.New("worker transfer already finished")
	}
	t.attempted = true
	if err := closeFiles(t.namespace, t.child); err != nil {
		t.namespace, t.child = nil, nil
		t.failed = err
		return err
	}
	t.namespace, t.child = nil, nil
	if child == nil {
		t.failed = errors.New("missing actual started worker")
		return t.failed
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	a := t.source
	a.mu.Lock()
	defer a.mu.Unlock()
	fail := func(e error) error { t.failed = e; return e }
	if err := a.check(); err != nil {
		return fail(err)
	}
	pin, err := pinProcess(a.root, child.Pid)
	if err != nil {
		return fail(err)
	}
	t.worker = pin
	if pin.identity.Parent != a.child.identity.PID || pin.identity.Namespace != a.child.identity.Namespace {
		return fail(errors.New("worker differs from actual original controller child scope"))
	}
	record := workerHandoffFrame{1, t.transfer, a.boot, a.launch, a.child.identity, pin.identity, t.request, t.descriptors}
	frame, err := json.Marshal(record)
	if err != nil {
		return fail(err)
	}
	if len(frame) > launchFrameLimit {
		return fail(errors.New("worker handoff frame exceeds limit"))
	}
	if err := pin.check(); err != nil {
		return fail(err)
	}
	if err := a.check(); err != nil {
		return fail(err)
	}
	n, err := unix.SendmsgN(int(t.parent.Fd()), frame, nil, nil, unix.MSG_DONTWAIT|unix.MSG_NOSIGNAL)
	if err != nil {
		return fail(err)
	}
	if n != len(frame) {
		return fail(errors.New("short original worker handoff"))
	}
	t.published = true
	return nil
}

// Check is latched into the SAME original per-execution checkpoint closure by
// its owner. It does not check receipt/storage/scope itself; those ORIGINAL
// owner checks remain required around this check. Loss is sticky, no repair.
func (t *WorkerHandoffTransfer) Check() error {
	if t == nil {
		return errors.New("missing original worker transfer")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed || !t.published {
		return errors.New("original worker handoff unavailable")
	}
	if t.failed != nil {
		return t.failed
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	a := t.source
	a.mu.Lock()
	defer a.mu.Unlock()
	t.failed = a.check()
	if t.failed == nil {
		t.failed = t.worker.check()
	}
	return t.failed
}

func (t *WorkerHandoffTransfer) Close() error {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil
	}
	t.closed = true
	return errors.Join(closeFiles(t.namespace, t.child, t.parent), t.worker.close())
}

// WorkerHandoff retains observation from the original private transfer. This
// is NOT a LaunchAnchor constructor or admission token. Explicit SDK use must
// still reach the SAME live parent receipt/storage/scope/checkpoint closure.
// Only optional namespace/bootstrap originals transfer ownership at intake;
// payload/checkpoint/view/phase retain their existing independent owners.
type WorkerHandoff struct {
	mu                               sync.Mutex
	root, namespace                  *os.File
	controller, worker               *processPin
	boot, controllerLaunch, transfer string
	request                          WorkerHandoffRequest
	checkpoint                       workerDescriptor // SAME verified frame snapshot, never rebaseline
	checkpointBindingAttempted       bool
	checkpointOwner                  *workercheckpoint.Channel // BORROWED sole physical owner
	closed                           bool
	failed                           error
}

// ConsumeWorkerHandoff runs after the caller's original bounded canonical
// input decode and mandatory payload/checkpoint/phase CLOEXEC claim, BEFORE
// LoadRuntime or any dependency child. It hashes but never retains/logs secret
// input. claimed=false leaves BOTH optional originals untouched/no wait;
// claimed=true transfers BOTH even on error. No received PID/namespace/Hello
// constructor or public request can independently establish source admission.
func ConsumeWorkerHandoff(request WorkerHandoffRequest, canonicalPrivateInput []byte) (w *WorkerHandoff, claimed bool, err error) {
	return consumeWorkerHandoff(request, canonicalPrivateInput, 5*time.Second)
}

func consumeWorkerHandoff(request WorkerHandoffRequest, input []byte, budget time.Duration) (w *WorkerHandoff, claimed bool, err error) {
	if err = request.validate(); err != nil {
		return nil, false, err
	}
	if len(input) == 0 || len(input) > 4<<20 || !json.Valid(input) {
		return nil, false, errors.New("invalid bounded private worker input")
	}
	sum := sha256.Sum256(input)
	if hex.EncodeToString(sum[:]) != request.InputDigest {
		return nil, false, errors.New("original private worker input digest mismatch")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer func() {
		if err != nil && w != nil {
			err = errors.Join(err, w.Close())
			w = nil
		}
	}()
	parentPID := os.Getppid()
	before, err := inspectPair(request.NamespaceFD, request.BootstrapFD, parentPID)
	if err != nil {
		return nil, false, err
	}
	descriptors, err := consumerWorkerDescriptors(request)
	if err != nil {
		return nil, false, err
	}
	// Checkpoint socket creation-peer must be the SAME original controller,
	// not merely a stream with the expected index. Never read checkpoint bytes.
	checkpointPeer, err := unix.GetsockoptUcred(4, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil || *checkpointPeer != before.creator {
		return nil, false, errors.New("worker checkpoint and bootstrap have different original creators")
	}
	self, err := captureSource()
	if err != nil {
		return nil, false, err
	}
	defer func() { err = errors.Join(err, self.Close()) }()
	controller, err := pinProcess(self.root, parentPID)
	if err != nil {
		return nil, false, err
	}
	defer func() {
		if controller != nil {
			err = errors.Join(err, controller.close())
		}
	}()
	if controller.identity.Namespace != before.namespace || self.launcher.identity.Namespace != before.namespace || self.launcher.identity.Parent != controller.identity.PID {
		return nil, false, errors.New("worker is outside original controller process/namespace")
	}
	if err = controller.check(); err != nil {
		return nil, false, err
	}
	if err = self.check(); err != nil {
		return nil, false, err
	}
	if err = checkActiveNamespace(self.root, before.namespace); err != nil {
		return nil, false, err
	}
	after, err := inspectPair(request.NamespaceFD, request.BootstrapFD, parentPID)
	if err != nil || before != after {
		return nil, false, errors.Join(errors.New("worker pair changed before claim"), err)
	}
	repeated, err := consumerWorkerDescriptors(request)
	if err != nil || repeated != descriptors {
		return nil, false, errors.Join(errors.New("worker infrastructure changed before claim"), err)
	}
	claimed = true
	w = &WorkerHandoff{controller: controller, boot: self.boot, request: request}
	controller = nil
	for _, fd := range []int{request.NamespaceFD, request.BootstrapFD} {
		if _, err = unix.FcntlInt(uintptr(fd), unix.F_SETFD, unix.FD_CLOEXEC); err != nil {
			_ = unix.Close(request.NamespaceFD)
			_ = unix.Close(request.BootstrapFD)
			return w, true, err
		}
	}
	w.namespace = os.NewFile(uintptr(request.NamespaceFD), "original-worker-pid-namespace")
	transport := os.NewFile(uintptr(request.BootstrapFD), "original-worker-bootstrap")
	defer func() { err = errors.Join(err, transport.Close()) }()
	w.root, err = duplicate(self.root)
	if err != nil {
		return w, true, err
	}
	w.worker, err = cloneProcess(self.launcher)
	if err != nil {
		return w, true, err
	}
	frame, cred, err := receiveLaunchFrame(request.BootstrapFD, time.Now().Add(budget))
	if err != nil {
		return w, true, err
	}
	if cred != before.creator {
		return w, true, errors.New("worker bootstrap sender differs from original creator")
	}
	record, err := decodeWorkerHandoff(frame)
	if err != nil {
		return w, true, err
	}
	if record.Request != request || record.Descriptors != descriptors || record.Boot != w.boot || record.Controller != w.controller.identity || record.Worker != w.worker.identity {
		return w, true, errors.New("worker handoff differs from original request/process/descriptors")
	}
	w.controllerLaunch, w.transfer = record.ControllerLaunch, record.Transfer
	w.checkpoint = record.Descriptors.Checkpoint
	if err = w.check(); err != nil {
		return w, true, err
	}
	return w, true, nil
}

func decodeWorkerHandoff(raw []byte) (workerHandoffFrame, error) {
	var record workerHandoffFrame
	if len(raw) == 0 || len(raw) > launchFrameLimit {
		return record, errors.New("invalid worker handoff size")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&record); err != nil {
		return workerHandoffFrame{}, err
	}
	encoded, err := json.Marshal(record)
	if err != nil || !bytes.Equal(encoded, raw) || record.Version != 1 || !canonicalHex(record.Transfer, 16) || !canonicalHex(record.ControllerLaunch, 16) {
		return workerHandoffFrame{}, errors.New("noncanonical private worker handoff")
	}
	if err := record.Request.validate(); err != nil {
		return workerHandoffFrame{}, err
	}
	return record, nil
}

func (w *WorkerHandoff) check() error {
	if w == nil || w.closed {
		return errors.New("original worker handoff closed")
	}
	if w.failed != nil {
		return w.failed
	}
	w.failed = w.checkOriginal()
	return w.failed
}

func (w *WorkerHandoff) checkOriginal() error {
	if w.root == nil || w.namespace == nil || w.controller == nil || w.worker == nil || w.controllerLaunch == "" || w.transfer == "" {
		return errors.New("incomplete original worker handoff")
	}
	if os.Getpid() != w.worker.identity.PID || os.Getppid() != w.controller.identity.PID {
		return errors.New("worker handoff used outside original child")
	}
	boot, err := bootIdentity(w.root)
	if err != nil || boot != w.boot {
		return errors.Join(errors.New("original worker boot changed"), err)
	}
	if err := w.controller.check(); err != nil {
		return err
	}
	if err := w.worker.check(); err != nil {
		return err
	}
	id, err := pidNamespace(w.namespace)
	if err != nil || id != w.worker.identity.Namespace || id != w.controller.identity.Namespace {
		return errors.Join(errors.New("worker left original controller namespace"), err)
	}
	return checkActiveNamespace(w.root, id)
}

func (w *WorkerHandoff) Check() error {
	if w == nil {
		return errors.New("missing original worker handoff")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.check()
}

func (w *WorkerHandoff) DuplicatePIDNamespace() (out *os.File, err error) {
	if w == nil {
		return nil, errors.New("missing original worker handoff")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	w.mu.Lock()
	defer w.mu.Unlock()
	if err = w.check(); err != nil {
		return nil, err
	}
	out, err = duplicate(w.namespace)
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
	if err != nil || id != w.worker.identity.Namespace {
		return out, errors.Join(errors.New("worker namespace duplicate changed identity"), err)
	}
	err = w.check()
	return out, err
}

func (w *WorkerHandoff) Close() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	return errors.Join(closeFiles(w.root, w.namespace), w.controller.close(), w.worker.close())
}
