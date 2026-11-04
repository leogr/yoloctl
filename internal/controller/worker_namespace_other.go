//go:build !linux

package controller

import (
	"errors"
	"os"
)

type WorkerPIDNamespace struct{}

func validateWorkerPIDNamespace(*os.File) error {
	return errors.New("native worker PID namespace requires Linux")
}
func ClaimWorkerPIDNamespace(work WorkerInput) (*WorkerPIDNamespace, error) {
	if work.PIDNamespaceFD != 0 {
		return nil, errors.New("native worker PID namespace requires Linux")
	}
	return nil, nil
}
func (*WorkerPIDNamespace) Close() error { return nil }
