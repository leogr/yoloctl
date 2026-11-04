//go:build !linux

package operator

import (
	"context"
	"errors"
)

// RestoreNative is available only inside the Linux space VM.
func RestoreNative(context.Context, RegistrationIdentity) error {
	return errors.New("native operator restoration requires VM infrastructure")
}
