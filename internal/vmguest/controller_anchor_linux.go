//go:build linux

package vmguest

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/leogr/yoloctl/internal/vmanchor"
)

// Initialized only during validated boot, before listenControl can expose the
// fixed controller launcher. It lives with PID1 across controller restarts.
var controllerBootSource *vmanchor.FirecrackerSource

func captureControllerBootSource() {
	source, err := vmanchor.CaptureFirecrackerSource()
	if err != nil {
		fmt.Fprintln(os.Stderr, "controller launch evidence unavailable:", err)
		return
	}
	controllerBootSource = source
}

type controllerLaunchTransfer interface {
	ChildFiles() []*os.File
	PublishStarted(*os.Process) error
	Close() error
}

func startAnchoredController(cmd *exec.Cmd) (controllerProcess, error) {
	var transfer controllerLaunchTransfer
	if controllerBootSource != nil {
		attempt, err := controllerBootSource.PrepareLaunch()
		if err == nil {
			transfer = attempt
		} else {
			fmt.Fprintln(os.Stderr, "controller launch evidence unavailable:", err)
		}
	}
	return startControllerAttempt(cmd, transfer, startOwned)
}

// Optional evidence cannot rewrite the actual Start result. In particular,
// every successful Start returns the real child for the existing sole Wait,
// even if publication or descriptor closure fails. No reaper lock is held
// during PublishStarted; startOwned has already returned and unlocked it.
func startControllerAttempt(cmd *exec.Cmd, transfer controllerLaunchTransfer, start func(*exec.Cmd) error) (controllerProcess, error) {
	if transfer != nil {
		// This reservation belongs only to the fixed exec with no other
		// inherited files. Never replace an unrelated launcher's ExtraFiles.
		if len(cmd.ExtraFiles) != 0 {
			_ = transfer.Close()
			transfer = nil
		} else {
			cmd.ExtraFiles = transfer.ChildFiles()
		}
	}
	if transfer != nil {
		defer func() {
			if err := transfer.Close(); err != nil {
				fmt.Fprintln(os.Stderr, "controller launch evidence close failed:", err)
			}
		}()
	}
	if err := start(cmd); err != nil {
		return nil, err
	}
	if transfer != nil {
		if err := transfer.PublishStarted(cmd.Process); err != nil {
			fmt.Fprintln(os.Stderr, "controller launch evidence unavailable:", err)
		}
	}
	fmt.Fprintln(os.Stderr, "controller started:", cmd.Process.Pid)
	return controllerChild{cmd}, nil
}
