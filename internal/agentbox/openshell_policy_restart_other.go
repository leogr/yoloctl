//go:build !linux

package agentbox

import (
	"context"
	"errors"
)

func (a LiveApplier) preflightNativePolicyRestart(context.Context, *Config, []byte) error {
	return errors.New("native policy restart qualification requires the Linux VM controller")
}
func (a LiveApplier) restartNativeGeneration(context.Context, *Config, []byte) error {
	return errors.New("native policy restart requires the Linux VM controller")
}

func (a LiveApplier) startNativeStoppedGeneration(context.Context, *Config, []byte) error {
	return errors.New("fresh native generation Start requires the Linux VM controller")
}

func (a LiveApplier) prepareNativeSavedStart(context.Context, *Config, bool) (*NativePreparedStart, error) {
	return nil, errors.New("native saved-policy Start requires the Linux VM controller")
}
