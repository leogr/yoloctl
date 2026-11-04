//go:build !linux && !darwin

package workercheckpoint

import (
	"context"
	"errors"
)

func (*Channel) initialize() (fileIdentity, error) {
	return fileIdentity{}, errors.New("private checkpoints require supported Unix")
}
func (*Channel) readPart(context.Context, []byte) (int, error) {
	return 0, errors.New("private checkpoints require supported Unix")
}
func (*Channel) writePart(context.Context, []byte) (int, error) {
	return 0, errors.New("private checkpoints require supported Unix")
}
