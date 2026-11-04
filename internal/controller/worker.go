package controller

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"github.com/leogr/yoloctl/internal/termio"
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
	work := WorkerInput{Principal: p, Request: req, NativePhaseFD: phaseFD, NativePhasePipe: phasePipe}
	var prepared workerPreparedHandoff
	var privateInput []byte
	if streams.WorkerHandoff != nil {
		candidateWork := work
		candidateWork.PIDNamespaceFD = 3 + len(cmd.ExtraFiles)
		candidateWork.PIDNamespaceBootstrapFD = candidateWork.PIDNamespaceFD + 1
		var phaseChild *os.File
		if phaseFD != 0 {
			_, phaseChild = observation.pipe()
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
	cmd.WaitDelay = 5 * time.Second
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	var terminal *os.File
	var stdinRead, stdinWrite *os.File
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
		cmd.Stdin, cmd.Stdout, cmd.Stderr = stdinRead, streams.Out, streams.Err
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		err = cmd.Start()
	}
	if err != nil {
		return 2, err
	}
	// Optional publication retains the actual child and sole original Wait.
	if prepared != nil {
		_ = prepared.PublishStarted(cmd.Process)
	}
	observation.Started(cmd.Process.Pid)
	closePhaseWrite()
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
	if terminal != nil {
		outputs.Add(1)
		go func() {
			defer outputs.Done()
			_, _ = io.Copy(streams.Out, terminal)
		}()
		go func() { _, _ = io.Copy(terminal, streams.In) }()
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
	observation.Exited()
	cancelWorker()
	// Terminate transient children before joining their inherited checkpoint
	// descriptors. A killed worker cannot leave a child keeping this read open.
	signalGroup(syscall.SIGKILL)
	select {
	case <-releaseDone:
	case <-time.After(time.Second):
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
	select {
	case <-drained:
	case <-time.After(5 * time.Second):
		if terminal != nil {
			_ = terminal.Close()
		}
		<-drained
		if err == nil {
			err = errors.New("worker exited with an inherited terminal still open")
		}
	}
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
