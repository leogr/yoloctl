package openshell

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

type runtimeProcess struct {
	role        runtimeChildRole
	process     *os.Process
	originalPID int // Process.Release invalidates process.Pid after our sole wait.
	exited      bool
	status      syscall.WaitStatus
}

// startRuntimeChild starts only the exact helper's fixed infrastructure role.
// Neither its argv nor its environment can select a workload. Real workloads
// remain behind the qualified boundary and supervisor SSH.
func startRuntimeChild(material runtimeMaterial, role runtimeChildRole, shared runtimeShared, artifacts *os.File) (*runtimeProcess, error) {
	if os.Getpid() != 1 || os.Geteuid() != 0 || os.Getegid() != 0 {
		return nil, errors.New("runtime child creation requires root PID 1")
	}
	payload, err := material.childPayload(role)
	if err != nil {
		return nil, err
	}
	sealed, err := sealedRuntimeChild(payload)
	if err != nil {
		return nil, err
	}
	defer func() { _ = sealed.Close() }()
	args, err := material.record.Launch.HelperLaunch().Args()
	if err != nil {
		return nil, err
	}
	args[2] = string(role)
	run := shared.supervisorRun
	if role == runtimeBoundary {
		run = shared.control
	}
	if shared.control == nil || run == nil || artifacts == nil {
		return nil, errors.New("runtime child lacks its exact private mount sources")
	}
	cmd := exec.Command("/proc/self/exe", args[1:]...)
	cmd.Dir = "/"
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C"}
	cmd.ExtraFiles = []*os.File{sealed, shared.control, run, artifacts}
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	// No parent-liveness pipe, attach/controller context or restart policy.
	// PID 1 owns explicit stop; container PID-namespace destruction owns death.
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &runtimeProcess{role: role, process: cmd.Process, originalPID: cmd.Process.Pid}, nil
}

// PID 1 owns one wait loop for both named children and orphan descendants. No
// Cmd.Wait goroutine competes for a child's status, and no old PID is adopted.
func reapRuntimeChildren(children []*runtimeProcess) error {
	if os.Getpid() != 1 || os.Geteuid() != 0 {
		return errors.New("runtime child reaping requires root PID 1")
	}
	for {
		var status syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &status, syscall.WNOHANG, nil)
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if errors.Is(err, syscall.ECHILD) {
			for _, child := range children {
				if !child.exited {
					return errors.New("runtime child disappeared without its own wait status")
				}
			}
			return nil
		}
		if err != nil {
			return err
		}
		if pid == 0 {
			return nil
		}
		for _, child := range children {
			if child.exited || child.originalPID != pid {
				continue
			}
			// An exited child's numeric PID can later name an orphan. Reap it
			// as an orphan, never adopt it or change the original child's status.
			child.exited, child.status = true, status
			// Wait4 above consumed this exact child. Release its Go pidfd handle;
			// no further signal can be sent through a reused numeric process ID.
			if err := child.process.Release(); err != nil {
				return err
			}
		}
	}
}

func signalRuntimeChildren(children []*runtimeProcess, signal os.Signal) error {
	if os.Getpid() != 1 || os.Geteuid() != 0 || os.Getegid() != 0 {
		return errors.New("runtime child signaling requires root PID 1")
	}
	var err error
	for _, child := range children {
		if child.exited {
			continue
		}
		if signalErr := child.process.Signal(signal); signalErr != nil && !errors.Is(signalErr, os.ErrProcessDone) {
			err = errors.Join(err, signalErr)
		}
	}
	return err
}

func stopRuntimeChildren(children []*runtimeProcess) error {
	if len(children) == 0 {
		return nil
	}
	err := signalRuntimeChildren(children, syscall.SIGTERM)
	for _, stage := range []struct {
		duration time.Duration
		force    bool
	}{{5 * time.Second, false}, {3 * time.Second, true}} {
		if stage.force {
			err = errors.Join(err, signalRuntimeChildren(children, syscall.SIGKILL))
		}
		deadline := time.Now().Add(stage.duration)
		for {
			if reapErr := reapRuntimeChildren(children); reapErr != nil {
				return errors.Join(err, reapErr)
			}
			alive := false
			for _, child := range children {
				alive = alive || !child.exited
			}
			if !alive {
				return err
			}
			if !time.Now().Before(deadline) {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	return errors.Join(err, errors.New("runtime children did not terminate within explicit stop bounds"))
}
