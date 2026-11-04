//go:build !linux

package main

import (
	"context"
	"errors"
)

// diskUsage is only measured inside Linux VM controllers.
func diskUsage(context.Context, string) (uint64, error) {
	return 0, errors.New("disk usage is only measured inside the VM")
}
