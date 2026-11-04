//go:build !linux

package main

import (
	"errors"
	"github.com/leogr/yoloctl/internal/controller"
)

func consumeEarlyWorkerLaunch(controller.WorkerInput, string) *workerLaunchCandidate {
	return captureWorkerLaunchCandidate(func() (workerLaunchObservation, bool, error) {
		return nil, false, errors.New("original worker handoff requires Linux")
	})
}
