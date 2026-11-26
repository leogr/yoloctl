package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"github.com/leogr/yoloctl/internal/termio"
	"golang.org/x/sys/unix"
)

// WorkerInput is transported over a private inherited descriptor. Sensitive
// values never become worker argv, environment variables or operation receipts.
type WorkerInput struct {
	Principal Principal `json:"principal"`
	Request   Request   `json:"request"`
	// Original private ExtraFiles index, never a public request/argv setting.
	PIDNamespaceFD          int                `json:"pid_namespace_fd,omitempty"`
	PIDNamespaceBootstrapFD int                `json:"pid_namespace_bootstrap_fd,omitempty"`
	WorkerAssociationDigest string             `json:"worker_association_digest,omitempty"`
	NativePhaseFD           int                `json:"native_phase_fd,omitempty"`
	NativePhasePipe         *NativePhasePipeID `json:"native_phase_pipe,omitempty"`
	// LogFD is the write end of the worker's structured log pipe. It is the
	// LAST inherited descriptor, after every slot above, so it never moves
	// the payload/checkpoint/view/phase/namespace layout.
	LogFD   int                `json:"log_fd,omitempty"`
	LogPipe *NativePhasePipeID `json:"log_pipe,omitempty"`
}

// WorkerPIDAnchor is supplied only by actual controller launch admission.
// Fixtures may exercise transport with their own namespace but cannot qualify
// a VM launch. There is no constructor from received numeric identities here.
type WorkerPIDAnchor interface {
	Check() error
	DuplicatePIDNamespace() (*os.File, error)
}

// RunWorker gives each shared-CLI invocation its own process and stdio, keeping
// Cobra flag state and process-local configuration isolated between requests.
// The binary path and arguments are fixed by trusted infrastructure.
func RunWorker(ctx context.Context, p Principal, req Request, streams Streams) (int, error) {
	defer streams.NativePhase.Exited()
	observation := streams.NativePhase
	if observation != nil && !observation.matches(p, req) {
		observation.markUnavailable()
		observation = nil
	}
	workerCtx, cancelWorker := context.WithCancel(ctx)
	defer cancelWorker()
	executable, err := os.Executable()
	if err != nil {
		return 2, err
	}
	payloadRead, payloadWrite, err := os.Pipe()
	if err != nil {
		return 2, err
	}
	defer func() { _ = payloadRead.Close() }()
	defer func() { _ = payloadWrite.Close() }()
	releasedRead, releasedWrite, err := termio.ViewPair()
	if err != nil {
		return 2, err
	}
	defer func() { _ = releasedRead.Close() }()
	defer func() { _ = releasedWrite.Close() }()
	logRead, logWrite, err := os.Pipe()
	if err != nil {
		return 2, err
	}
	defer func() { _ = logRead.Close() }()
	defer func() { _ = logWrite.Close() }()
	logPipe, _ := nativePhasePipeIdentity(int(logWrite.Fd()))
	logSink := streams.Log
	if logSink == nil {
		logSink = plainLogSink(streams.Err)
	}
	cmd := exec.CommandContext(workerCtx, executable, "controller-internal", "worker")
	cmd.Env = []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=/root", "LANG=C.UTF-8", "TERM=" + req.Term, "YOLOCTL_TUI=1",
		spaceenv.ContextEnv + "=" + spaceenv.ContextPath,
	}
	cmd.ExtraFiles = []*os.File{payloadRead, releasedWrite}
	var viewParent, viewChild *os.File
	var viewDone chan struct{}
	if req.View {
		viewParent, viewChild, err = termio.ViewPair()
		if err != nil {
			return 2, err
		}
		defer func() { _ = viewParent.Close() }()
		defer func() { _ = viewChild.Close() }()
		cmd.ExtraFiles = append(cmd.ExtraFiles, viewChild)
	}
	// Phase is appended FIRST; the optional namespace/bootstrap pair follows it.
	phaseFD, phasePipe, closePhaseWrite := prepareNativePhaseTransport(cmd, observation)
	defer closePhaseWrite()
	work := WorkerInput{Principal: p, Request: req, NativePhaseFD: phaseFD, NativePhasePipe: phasePipe, LogPipe: logPipe}
	var prepared workerPreparedHandoff
	var privateInput []byte
	if streams.WorkerHandoff != nil {
		candidateWork := work
		candidateWork.PIDNamespaceFD = 3 + len(cmd.ExtraFiles)
		candidateWork.PIDNamespaceBootstrapFD = candidateWork.PIDNamespaceFD + 1
		candidateWork.LogFD = candidateWork.PIDNamespaceBootstrapFD + 1
		var phaseChild *os.File
		if phaseFD != 0 {
			phaseChild = observation.pipe()
		}
		candidate, proofErr := streams.WorkerHandoff.Prepare(candidateWork, workerHandoffFiles{Payload: payloadRead, Checkpoint: releasedWrite, View: viewChild, Phase: phaseChild})
		if proofErr == nil && candidate != nil {
			prepared = candidate
			work = prepared.Input()
			privateInput = prepared.CanonicalInput()
			cmd.ExtraFiles = append(cmd.ExtraFiles, prepared.ChildFiles()...)
			defer func() { _ = prepared.Close() }()
		} else if candidate != nil {
			_ = candidate.Close()
		}
	}
	// Optional unavailable launch proof must not change ordinary worker/CLI
	// startup. Its later explicit SDK check refuses; it is never empty census.
	var namespaceChild *os.File
	namespaceIndex := 0
	if streams.WorkerHandoff == nil && streams.PIDAnchor != nil && streams.PIDAnchor.Check() == nil {
		candidate, duplicateErr := streams.PIDAnchor.DuplicatePIDNamespace()
		if duplicateErr == nil && candidate != nil && validateWorkerPIDNamespace(candidate) == nil && streams.PIDAnchor.Check() == nil {
			namespaceChild = candidate
			namespaceIndex = 3 + len(cmd.ExtraFiles)
			cmd.ExtraFiles = append(cmd.ExtraFiles, namespaceChild)
		} else if candidate != nil {
			_ = candidate.Close() // only the accessor's original owned duplicate
		}
	}
	if namespaceChild != nil {
		defer func() { _ = namespaceChild.Close() }()
	}
	if namespaceIndex != 0 {
		work.PIDNamespaceFD = namespaceIndex
	}
	// The log pipe is appended last; a prepared handoff already fixed its slot
	// in the canonical input after the transferred pair.
	logIndex := 3 + len(cmd.ExtraFiles)
	if prepared != nil {
		if work.LogFD != logIndex {
			return 2, errors.New("worker log descriptor differs from the prepared handoff")
		}
	} else {
		work.LogFD = logIndex
	}
	cmd.ExtraFiles = append(cmd.ExtraFiles, logWrite)
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	var terminal *os.File
	var stdinRead, stdinWrite *os.File
	var stdoutRead, stdoutWrite, stderrRead, stderrWrite *os.File
	if req.Terminal {
		terminal, err = pty.StartWithSize(cmd, &pty.Winsize{Cols: req.Cols, Rows: req.Rows})
	} else {
		// A caller may keep stdin open after a command exits. Give the child a
		// descriptor so os/exec does not wait for a blocked stdin copy goroutine.
		stdinRead, stdinWrite, err = os.Pipe()
		if err != nil {
			return 2, err
		}
		defer func() { _ = stdinRead.Close(); _ = stdinWrite.Close() }()
		// The worker writes into pipes this function owns, so os/exec starts no
		// copier and Wait returns when the worker itself exits. Its output is
		// joined after the group is killed, so a transient child that inherited
		// stdout cannot hold a finished command.
		stdoutRead, stdoutWrite, err = os.Pipe()
		if err != nil {
			return 2, err
		}
		defer func() { _ = stdoutRead.Close(); _ = stdoutWrite.Close() }()
		stderrRead, stderrWrite, err = os.Pipe()
		if err != nil {
			return 2, err
		}
		defer func() { _ = stderrRead.Close(); _ = stderrWrite.Close() }()
		cmd.Stdin, cmd.Stdout, cmd.Stderr = stdinRead, stdoutWrite, stderrWrite
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		err = cmd.Start()
	}
	if err != nil {
		return 2, err
	}
	// The PTY master is read and written through a pollable duplicate, so an
	// expired output join can wake a read that a process outside the worker's
	// group keeps waiting.
	var master *os.File
	if terminal != nil {
		if master, err = pollableDup(terminal); err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			_ = terminal.Close()
			return 2, err
		}
		defer func() { _ = master.Close() }()
	}
	// Optional publication retains the actual child and sole original Wait.
	if prepared != nil {
		_ = prepared.PublishStarted(cmd.Process)
	}
	observation.Started(cmd.Process.Pid)
	closePhaseWrite()
	_ = logWrite.Close()
	logDone := make(chan struct{})
	var logErr error
	go func() {
		defer close(logDone)
		logErr = relayWorkerLog(context.WithoutCancel(ctx), logRead, logSink)
	}()
	if req.View {
		_ = viewChild.Close()
		if streams.View == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return 2, errors.New("missing worker session channel")
		}
		defer func() { _ = streams.View.Close() }()
		viewDone = make(chan struct{})
		go func() { defer close(viewDone); _, _ = io.Copy(streams.View, viewParent) }()
		go func() { _, _ = io.Copy(viewParent, streams.View) }()
	}
	if stdinWrite != nil {
		_ = stdinRead.Close()
		go func() { _, _ = io.Copy(stdinWrite, streams.In); _ = stdinWrite.Close() }()
	}
	if stdoutWrite != nil {
		// Only the worker and its descendants keep the write ends now.
		_ = stdoutWrite.Close()
		_ = stderrWrite.Close()
	}
	if terminal != nil {
		defer func() { _ = terminal.Close() }()
	}
	// The worker and its transient subprocesses are one process group. Holders
	// are independent systemd units, so canceling an attach preserves them.
	done := make(chan struct{})
	controlsDone := make(chan struct{})
	var stopOnce sync.Once
	stopControls := func() { stopOnce.Do(func() { close(done) }); <-controlsDone }
	defer stopControls()
	signalGroup := func(sig syscall.Signal) { _ = syscall.Kill(-cmd.Process.Pid, sig) }
	go func() {
		select {
		case <-workerCtx.Done():
			signalGroup(syscall.SIGTERM)
			timer := time.NewTimer(3 * time.Second)
			defer timer.Stop()
			select {
			case <-done:
			case <-timer.C:
				signalGroup(syscall.SIGKILL)
			}
		case <-done:
		}
	}()
	_ = payloadRead.Close()
	_ = releasedWrite.Close()
	go func() {
		if prepared != nil {
			framed := append(append([]byte(nil), privateInput...), '\n')
			_ = writeAll(payloadWrite, framed)
			clear(framed)
			clear(privateInput)
		} else {
			_ = json.NewEncoder(payloadWrite).Encode(work)
		}
		_ = payloadWrite.Close()
	}()
	releaseDone := make(chan struct{})
	var checkpointErr error
	go func() {
		defer close(releaseDone)
		checkpointErr = serveWorkerCheckpoints(workerCtx, releasedRead, streams)
		if checkpointErr != nil {
			_ = releasedRead.Close()
			cancelWorker()
		}
	}()
	var outputs sync.WaitGroup
	// After an expired output join each source decides by its own state
	// whether a process outside the worker's group still holds it open.
	var expired, heldOpen atomic.Bool
	var sources []*os.File
	copyOut := func(dst io.Writer, src *os.File) {
		sources = append(sources, src)
		outputs.Add(1)
		if dst == nil {
			dst = io.Discard
		}
		go func() {
			defer outputs.Done()
			left := -1
			_, _ = io.Copy(dst, drainSource{src, &expired, &heldOpen, &left})
		}()
	}
	if terminal != nil {
		copyOut(streams.Out, master)
		go func() { _, _ = io.Copy(master, streams.In) }()
	} else {
		copyOut(streams.Out, stdoutRead)
		copyOut(streams.Err, stderrRead)
	}
	go func() {
		defer close(controlsDone)
		sizes, signals := streams.Resize, streams.Signals
		for {
			select {
			case <-done:
				return
			case size, ok := <-sizes:
				if !ok {
					sizes = nil
					continue
				}
				if terminal != nil {
					_ = pty.Setsize(terminal, &pty.Winsize{Cols: size.Cols, Rows: size.Rows})
				}
			case sig, ok := <-signals:
				if !ok {
					signals = nil
					continue
				}
				signalGroup(syscall.Signal(sig))
			}
		}
	}()
	err = cmd.Wait()
	exited := time.Now()
	observation.Exited()
	if streams.Exited != nil {
		streams.Exited()
	}
	cancelWorker()
	// From the worker's exit on, a client that stopped reading must not hold
	// the finished command: every client write is bounded by the fail-safe.
	// The deadline stays after return, through the caller's own joins; the
	// caller bounds its exit frame itself.
	drainUntil := func(t time.Time) {
		if streams.DrainDeadline != nil {
			streams.DrainDeadline(t)
		}
	}
	drainUntil(exited.Add(controllerFailSafe))
	// Terminate transient children before joining their inherited checkpoint
	// descriptors. A killed worker cannot leave a child keeping this read open.
	signalGroup(syscall.SIGKILL)
	checkpointWait := time.NewTimer(controllerFailSafe)
	defer checkpointWait.Stop()
	select {
	case <-releaseDone:
	case <-checkpointWait.C:
		_ = releasedRead.Close()
		<-releaseDone
		if checkpointErr == nil {
			checkpointErr = errors.New("worker checkpoint remained open after exit")
		}
	}
	if req.View {
		select {
		case <-viewDone:
		case <-time.After(5 * time.Second):
			_ = viewParent.Close()
			_ = streams.View.Close()
			<-viewDone
		}
	}
	// The durable checkpoint callback and worker termination precede receipt finalization.
	stopControls() // no ioctl may race the terminal close below
	drained := make(chan struct{})
	go func() { outputs.Wait(); close(drained) }()
	// The output join has its own fail-safe, and so do the client writes in it.
	drainUntil(time.Now().Add(controllerFailSafe))
	outputWait := time.NewTimer(controllerFailSafe)
	defer outputWait.Stop()
	select {
	case <-drained:
	case <-outputWait.C:
		// A copy blocked on a client that stopped reading is released by the
		// expired write; that client is then detached like after any failed
		// write, and the copy reads the rest of its source. A read waiting on
		// a source is woken: a source with nothing to read and no end is held
		// open by a process outside the worker's group.
		drainUntil(time.Now())
		expired.Store(true)
		for _, src := range sources {
			_ = src.SetReadDeadline(time.Now())
		}
		<-drained
		if err == nil && heldOpen.Load() {
			if terminal != nil {
				err = errors.New("worker exited with an inherited terminal still open")
			} else {
				err = errors.New("worker exited with its output still open")
			}
		}
	}
	// Every record the worker wrote precedes its result: the controller sends
	// the exit frame only after this join. As when os/exec joined the output
	// inside Wait, a pipe-mode worker's log join starts after its output join.
	logFrom := exited
	if terminal == nil {
		logFrom = time.Now()
	}
	joinWorkerLog(logRead, logDone, &logErr, logFrom, logSink)
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code := exit.ExitCode()
		if code < 0 {
			code = 128
			if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() {
				code += int(status.Signal())
			}
			// A checkpoint fault often causes the termination; keep both, so
			// finalization still sees, e.g., a contradicted completion.
			return code, &WorkerInterrupted{Cause: errors.Join(err, checkpointErr)}
		}
		if checkpointErr != nil {
			return code, &WorkerInterrupted{Cause: checkpointErr}
		}
		return code, nil
	}
	if checkpointErr != nil {
		return 2, &WorkerInterrupted{Cause: checkpointErr}
	}
	if err != nil {
		return 2, &WorkerInterrupted{Cause: err}
	}
	return 0, nil
}

// errOutputHeld ends a copy whose source a process outside the worker's group
// still holds open after the output join expired.
var errOutputHeld = errors.New("worker output held open")

// drainSource reads a worker output source. Once the output join expired it
// reads only the bytes that were ready at that moment, then the source has
// ended or is held: a writer outside the group that keeps producing cannot
// extend the join. Each copier owns its left counter.
type drainSource struct {
	f       *os.File
	expired *atomic.Bool
	held    *atomic.Bool
	left    *int // bytes still to drain after expiry; -1 until expiry is seen
}

func (s drainSource) Read(p []byte) (int, error) {
	for {
		if s.expired.Load() {
			if *s.left < 0 {
				*s.left = available(s.f)
			}
			if *s.left == 0 {
				if ended(s.f) {
					return 0, io.EOF
				}
				s.held.Store(true)
				return 0, errOutputHeld
			}
			if len(p) > *s.left {
				p = p[:*s.left]
			}
			_ = s.f.SetReadDeadline(time.Time{})
		}
		n, err := s.f.Read(p)
		if n == 0 && errors.Is(err, os.ErrDeadlineExceeded) {
			continue // woken at expiry: the source's own state decides above
		}
		if *s.left > 0 {
			*s.left -= min(n, *s.left)
		}
		return n, err
	}
}

// ended reports, without waiting, whether f reached its end with nothing
// left to read: its writer side is closed.
func ended(f *os.File) bool {
	if available(f) != 0 {
		return false
	}
	raw, err := f.SyscallConn()
	if err != nil {
		return false
	}
	hup := false
	_ = raw.Control(func(fd uintptr) {
		for {
			fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
			n, err := unix.Poll(fds, 0)
			if errors.Is(err, unix.EINTR) {
				continue
			}
			hup = err == nil && n > 0 && fds[0].Revents&(unix.POLLHUP|unix.POLLERR) != 0
			return
		}
	})
	return hup
}

// pollableDup returns a non-blocking duplicate of f that the runtime poller
// serves, so reads on it honor deadlines. The original keeps its ioctls.
func pollableDup(f *os.File) (*os.File, error) {
	fd, err := syscall.Dup(int(f.Fd()))
	if err != nil {
		return nil, err
	}
	syscall.CloseOnExec(fd)
	if err := syscall.SetNonblock(fd, true); err != nil {
		_ = syscall.Close(fd)
		return nil, err
	}
	dup := os.NewFile(uintptr(fd), f.Name())
	if err := dup.SetReadDeadline(time.Time{}); err != nil {
		_ = dup.Close()
		return nil, fmt.Errorf("worker terminal is not pollable: %w", err)
	}
	return dup, nil
}
