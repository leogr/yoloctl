//go:build !linux

package agentbox

import (
	"context"
	"errors"

	"github.com/leogr/yoloctl/internal/container"
)

// BoxLauncher is a Linux VM controller operation.
func BoxLauncher(context.Context, *container.Client, string, *container.Details) (LauncherSupport, error) {
	return LauncherSupport{}, errors.New("the session launcher requires the Linux VM controller")
}

// OtherContainerProcesses is a Linux VM controller operation.
func OtherContainerProcesses(int) (int, error) {
	return 0, errors.New("process inventory requires the Linux VM controller")
}

// MainRecord is a Linux VM controller operation.
func MainRecord(context.Context, *container.Client, string, *container.Details) (string, string, error) {
	return "", "", errors.New("the session launcher requires the Linux VM controller")
}
