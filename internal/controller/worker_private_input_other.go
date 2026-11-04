//go:build !linux

package controller

import (
	"context"
	"errors"
	"os"
)

type WorkerPrivateIntake struct{}

func ClaimWorkerPrivateIntake(context.Context) (*WorkerPrivateIntake, bool, error) {
	return nil, false, errors.New("private worker intake requires Linux")
}
func (*WorkerPrivateIntake) CheckOriginal() error {
	return errors.New("private worker intake requires Linux")
}
func (*WorkerPrivateIntake) Checkpoint() (*os.File, error) {
	return nil, errors.New("private worker intake requires Linux")
}
func (*WorkerPrivateIntake) Decode(context.Context) (WorkerInput, string, error) {
	return WorkerInput{}, "", errors.New("private worker intake requires Linux")
}
func (*WorkerPrivateIntake) Close() error { return nil }

func (*WorkerPrivateIntake) TakeCheckpoint() (*os.File, error) {
	return nil, errors.New("private worker intake requires Linux")
}
