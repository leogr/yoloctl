//go:build !linux

package agentbox

import (
	"context"
	"errors"
)

func (a LiveApplier) preflightQualifiedNativeCreation(context.Context, *Config, string, []byte) error {
	return errors.New("native creation qualification requires the Linux VM controller")
}

func (a LiveApplier) nativeCreationPrerequisite(context.Context) error {
	return errors.New("native creation qualification requires the Linux VM controller")
}

func (a LiveApplier) createQualifiedNativeContainer(context.Context, *Config, string, []byte) error {
	return errors.New("native creation requires the Linux VM controller")
}
