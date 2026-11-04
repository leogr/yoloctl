package controller

import (
	"os/exec"
	"sync"

	"github.com/leogr/yoloctl/internal/operator"
)

// NativePhasePipeID is supplied only by RunWorker's actual inheritance map.
// Matching an FD number alone is insufficient to own or close a descriptor.
type NativePhasePipeID struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}

func prepareNativePhaseTransport(cmd *exec.Cmd, observation *WorkerObservation) (int, *NativePhasePipeID, func()) {
	noop := func() {}
	if observation == nil || !nativePhaseTransportAvailable {
		return 0, nil, noop
	}
	_, write := observation.pipe()
	if write == nil {
		return 0, nil, noop
	}
	identity, ok := nativePhasePipeIdentity(int(write.Fd()))
	if !ok {
		observation.markUnavailable()
		_ = write.Close()
		return 0, nil, noop
	}
	nativePhaseEnlargePipe(int(write.Fd()))
	fd := 3 + len(cmd.ExtraFiles)
	cmd.ExtraFiles = append(cmd.ExtraFiles, write)
	return fd, identity, func() { _ = write.Close() }
}

func (o *WorkerObservation) markUnavailable() {
	if o == nil {
		return
	}
	o.store.mu.Lock()
	defer o.store.mu.Unlock()
	if entry := o.entryLocked(); entry != nil {
		entry.loss = "unavailable"
	}
}

type NativePhaseWriter struct {
	mu      sync.Mutex
	fd      int
	closed  bool
	flushed bool
}

// OpenNativePhaseWriter accepts only the trusted appended diagnostic pipe. A
// missing/invalid descriptor leaves request, authority and view FDs untouched.
// Ownership/CLOEXEC/nonblocking setup completes before any caller can exec.
func OpenNativePhaseWriter(work WorkerInput) *NativePhaseWriter {
	expected := 5
	if work.Request.View {
		expected = 6
	}
	if work.NativePhaseFD != expected || work.NativePhasePipe == nil {
		return nil
	}
	return nativePhaseOpenWriter(work.NativePhaseFD, *work.NativePhasePipe)
}

// Flush is one best-effort bounded write after lifecycle locks unwind. It has
// no verdict/acknowledgment and cannot change the operation or its checkpoints.
func (w *NativePhaseWriter) Flush(raw []byte) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || w.flushed {
		return
	}
	w.flushed = true
	defer w.closeLocked()
	if len(raw) == 0 || len(raw) > operator.NativePhaseMaxBytes {
		return
	}
	if _, err := operator.DecodeNativePhaseTranscript(raw); err != nil {
		return
	}
	// Raw writes remain nonblocking. EINTR, zero and short writes consume the
	// same finite budget; there is no readiness wait or background retry.
	for attempts := 0; len(raw) != 0 && attempts < 64; attempts++ {
		n, retry := nativePhaseWrite(w.fd, raw)
		if n > 0 {
			raw = raw[n:]
		}
		if !retry {
			return
		}
	}
}

func (w *NativePhaseWriter) closeLocked() {
	if !w.closed {
		w.closed = true
		nativePhaseClose(w.fd)
	}
}

func (w *NativePhaseWriter) Close() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closeLocked()
}
